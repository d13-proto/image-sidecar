package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxRequestBodyBytes    int64 = 32 << 20
	maxImageBytes          int64 = 10 << 20
	maxTotalImageBytes     int64 = 20 << 20
	maxRemoteImages              = 8
	maxImageConcurrency          = 4
	imageDownloadTimeout         = 15 * time.Second
	imageProcessingTimeout       = 45 * time.Second
)

type remoteImage struct {
	imageURL map[string]any
	rawURL   string
}

type budgetReader struct {
	reader    io.Reader
	remaining *atomic.Int64
}

func (r *budgetReader) Read(buffer []byte) (int, error) {
	count, err := r.reader.Read(buffer)
	if count > 0 && r.remaining.Add(-int64(count)) < 0 {
		return 0, &requestError{http.StatusRequestEntityTooLarge, "images_too_large", "远程图片总大小超过 20 MiB"}
	}
	return count, err
}

type sidecarHandler struct {
	proxy        *httputil.ReverseProxy
	imageClient  *http.Client
	allowedHosts map[string]struct{}
}

type requestError struct {
	status  int
	code    string
	message string
}

func (e *requestError) Error() string { return e.message }

func newSidecarHandler(upstream *url.URL, imageClient *http.Client, allowedHosts []string) http.Handler {
	hosts := make(map[string]struct{}, len(allowedHosts))
	for _, host := range allowedHosts {
		hosts[normalizeHost(host)] = struct{}{}
	}

	imageClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return &requestError{http.StatusBadGateway, "image_download_failed", "图片重定向次数过多"}
		}
		return validateImageURL(request.URL, hosts)
	}

	proxy := httputil.NewSingleHostReverseProxy(upstream)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalDirector(request)
		request.Host = upstream.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		writeOpenAIError(w, http.StatusBadGateway, "upstream_error", "CLIProxyAPI 上游请求失败")
	}

	return &sidecarHandler{proxy: proxy, imageClient: imageClient, allowedHosts: hosts}
}

func (h *sidecarHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
		h.proxy.ServeHTTP(w, r)
		return
	}

	if r.ContentLength > maxRequestBodyBytes {
		writeOpenAIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "请求体超过 32 MiB")
		return
	}
	limitedBody := http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	body, err := io.ReadAll(limitedBody)
	_ = limitedBody.Close()
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeOpenAIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "请求体超过 32 MiB")
			return
		}
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "无法读取请求体")
		return
	}
	body, err = h.transformChatCompletions(r.Context(), body)
	if err != nil {
		h.writeTransformError(w, err)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	h.proxy.ServeHTTP(w, r)
}

func (h *sidecarHandler) transformChatCompletions(ctx context.Context, body []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, &requestError{http.StatusBadRequest, "invalid_json", "请求体不是有效 JSON"}
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return nil, err
	}

	messages, ok := payload["messages"].([]any)
	if !ok {
		return body, nil
	}
	var remoteImages []remoteImage
	for _, messageValue := range messages {
		message, ok := messageValue.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := message["content"].([]any)
		if !ok {
			continue
		}
		for _, partValue := range parts {
			part, ok := partValue.(map[string]any)
			if !ok || part["type"] != "image_url" {
				continue
			}
			imageURL, ok := part["image_url"].(map[string]any)
			if !ok {
				continue
			}
			rawURL, ok := imageURL["url"].(string)
			if !ok || strings.HasPrefix(rawURL, "data:") {
				continue
			}
			remoteImages = append(remoteImages, remoteImage{imageURL: imageURL, rawURL: rawURL})
		}
	}
	if len(remoteImages) == 0 {
		return body, nil
	}
	if len(remoteImages) > maxRemoteImages {
		return nil, &requestError{http.StatusRequestEntityTooLarge, "too_many_images", "每个请求最多包含 8 张远程图片"}
	}
	if err := h.resolveRemoteImages(ctx, remoteImages); err != nil {
		return nil, err
	}
	return json.Marshal(payload)
}

func (h *sidecarHandler) resolveRemoteImages(parent context.Context, images []remoteImage) error {
	ctx, cancel := context.WithTimeout(parent, imageProcessingTimeout)
	defer cancel()
	var remaining atomic.Int64
	remaining.Store(maxTotalImageBytes)
	errorsByIndex := make([]error, len(images))
	semaphore := make(chan struct{}, maxImageConcurrency)
	var workers sync.WaitGroup

	for index := range images {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			dataURL, err := h.fetchDataURL(ctx, images[index].rawURL, &remaining)
			if err != nil {
				errorsByIndex[index] = err
				cancel()
				return
			}
			images[index].imageURL["url"] = dataURL
		}(index)
	}
	workers.Wait()

	for index, err := range errorsByIndex {
		if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			continue
		}
		var requestErr *requestError
		if errors.As(err, &requestErr) {
			return &requestError{
				status:  requestErr.status,
				code:    requestErr.code,
				message: fmt.Sprintf("第 %d 张图片：%s", index+1, requestErr.message),
			}
		}
		return err
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &requestError{http.StatusGatewayTimeout, "image_download_timeout", "图片处理总时间超过 45 秒"}
	}
	if ctx.Err() != nil {
		return &requestError{http.StatusBadRequest, "request_canceled", "请求已取消"}
	}
	return nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return &requestError{http.StatusBadRequest, "invalid_json", "请求体只能包含一个 JSON 值"}
	}
	return nil
}

func (h *sidecarHandler) fetchDataURL(parent context.Context, rawURL string, remaining *atomic.Int64) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", &requestError{http.StatusBadRequest, "invalid_image_url", "图片 URL 无效"}
	}
	if err := validateImageURL(parsed, h.allowedHosts); err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(parent, imageDownloadTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", &requestError{http.StatusBadRequest, "invalid_image_url", "图片 URL 无效"}
	}
	request.Header.Set("Accept", "image/*")
	response, err := h.imageClient.Do(request)
	if err != nil {
		var requestErr *requestError
		if errors.As(err, &requestErr) {
			return "", requestErr
		}
		if parent.Err() != nil {
			return "", parent.Err()
		}
		if isTimeoutError(err) {
			return "", &requestError{http.StatusGatewayTimeout, "image_download_timeout", "单张图片下载超过 15 秒"}
		}
		return "", &requestError{http.StatusBadGateway, "image_download_failed", "图片下载失败"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", &requestError{http.StatusBadGateway, "image_download_failed", "图片服务器返回非成功状态"}
	}
	if response.ContentLength > maxImageBytes {
		return "", &requestError{http.StatusRequestEntityTooLarge, "image_too_large", "单张图片超过 10 MiB"}
	}
	reader := &budgetReader{reader: response.Body, remaining: remaining}
	content, err := io.ReadAll(io.LimitReader(reader, maxImageBytes+1))
	if err != nil {
		var requestErr *requestError
		if errors.As(err, &requestErr) {
			return "", requestErr
		}
		if parent.Err() != nil {
			return "", parent.Err()
		}
		if isTimeoutError(err) {
			return "", &requestError{http.StatusGatewayTimeout, "image_download_timeout", "单张图片下载超过 15 秒"}
		}
		return "", &requestError{http.StatusBadGateway, "image_download_failed", "读取图片内容失败"}
	}
	if int64(len(content)) > maxImageBytes {
		return "", &requestError{http.StatusRequestEntityTooLarge, "image_too_large", "单张图片超过 10 MiB"}
	}

	headerMIME := ""
	if value := response.Header.Get("Content-Type"); value != "" {
		headerMIME, _, _ = mime.ParseMediaType(value)
	}
	sniffedMIME := http.DetectContentType(content)
	mimeType := headerMIME
	if supportedImageMIME(sniffedMIME) {
		mimeType = sniffedMIME
	} else if !supportedImageMIME(headerMIME) || sniffedMIME != "application/octet-stream" {
		return "", &requestError{http.StatusBadRequest, "unsupported_image_type", "图片类型不受支持或内容与声明不符"}
	}
	return fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(content)), nil
}

func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

func validateImageURL(parsed *url.URL, allowedHosts map[string]struct{}) error {
	if parsed == nil || parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return &requestError{http.StatusBadRequest, "invalid_image_url", "图片 URL 仅支持不带用户信息的 HTTP/HTTPS 地址"}
	}
	if _, ok := allowedHosts[normalizeHost(parsed.Hostname())]; !ok {
		return &requestError{http.StatusBadRequest, "image_host_not_allowed", "图片域名不在白名单中"}
	}
	return nil
}

func supportedImageMIME(value string) bool {
	switch strings.ToLower(value) {
	case "image/jpeg", "image/png", "image/webp", "image/heic", "image/heif":
		return true
	default:
		return false
	}
}

func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

func (h *sidecarHandler) writeTransformError(w http.ResponseWriter, err error) {
	if requestErr, ok := err.(*requestError); ok {
		writeOpenAIError(w, requestErr.status, requestErr.code, requestErr.message)
		return
	}
	writeOpenAIError(w, http.StatusInternalServerError, "image_transform_error", "图片转换失败")
}

func writeOpenAIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"code":    code,
		},
	})
}
