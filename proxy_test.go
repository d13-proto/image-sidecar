package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const onePixelPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func TestChatCompletionsConvertsRemoteImage(t *testing.T) {
	const expectedDataURL = "data:image/png;base64," + onePixelPNGBase64

	pngBytes, err := base64.StdEncoding.DecodeString(onePixelPNGBase64)
	if err != nil {
		t.Fatal(err)
	}
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	t.Cleanup(imageServer.Close)

	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		if r.Header.Get("Authorization") != "Bearer cliproxyapi-key" {
			t.Errorf("上游 Authorization 丢失: %q", r.Header.Get("Authorization"))
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Content []struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					ImageURL struct {
						URL    string `json:"url"`
						Detail string `json:"detail"`
					} `json:"image_url"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("上游收到无效 JSON: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.Model != "gemini-3.1-flash-image" {
			t.Errorf("模型名被修改: %q", body.Model)
		}
		if body.Messages[0].Content[0].Text != "生成像素图" {
			t.Errorf("文本被修改: %q", body.Messages[0].Content[0].Text)
		}
		image := body.Messages[0].Content[1]
		if image.Type != "image_url" || image.ImageURL.URL != expectedDataURL || image.ImageURL.Detail != "auto" {
			t.Errorf("图片转换结果错误: %+v", image)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	t.Cleanup(upstream.Close)

	handler := newTestHandler(t, upstream.URL, imageServer, "images.example.com")
	sidecar := httptest.NewServer(handler)
	t.Cleanup(sidecar.Close)

	request, err := http.NewRequest(http.MethodPost, sidecar.URL+"/v1/chat/completions", strings.NewReader(`{
		"model":"gemini-3.1-flash-image",
		"messages":[{"role":"user","content":[
			{"type":"text","text":"生成像素图"},
			{"type":"image_url","image_url":{"url":"http://images.example.com/pixel.png","detail":"auto"}}
		]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer cliproxyapi-key")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", response.StatusCode)
	}
	if !upstreamCalled {
		t.Fatal("请求没有到达上游")
	}
}

func TestChatCompletionsRejectsHostOutsideAllowlist(t *testing.T) {
	imageCalled := false
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imageCalled = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(imageServer.Close)
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(upstream.Close)

	sidecar := httptest.NewServer(newTestHandler(t, upstream.URL, imageServer, "images.example.com"))
	t.Cleanup(sidecar.Close)
	response, err := http.Post(sidecar.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{
		"model":"gemini-3.1-flash-image",
		"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"http://other.example.com/image.png"}}]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400", response.StatusCode)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "image_host_not_allowed" {
		t.Errorf("错误码 = %q", body.Error.Code)
	}
	if imageCalled || upstreamCalled {
		t.Fatalf("拒绝后仍发起请求: image=%v upstream=%v", imageCalled, upstreamCalled)
	}
}

func TestChatCompletionsRejectsOversizedImage(t *testing.T) {
	pngBytes, err := base64.StdEncoding.DecodeString(onePixelPNGBase64)
	if err != nil {
		t.Fatal(err)
	}
	oversized := make([]byte, (10<<20)+1)
	copy(oversized, pngBytes)
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(oversized)
	}))
	t.Cleanup(imageServer.Close)
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	sidecar := httptest.NewServer(newTestHandler(t, upstream.URL, imageServer, "images.example.com"))
	t.Cleanup(sidecar.Close)
	response, err := http.Post(sidecar.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{
		"model":"gemini-3.1-flash-image",
		"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"http://images.example.com/large.png"}}]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("状态码 = %d，期望 413", response.StatusCode)
	}
	if upstreamCalled {
		t.Fatal("超限图片仍被转发到上游")
	}
}

func TestChatCompletionsBlocksPrivateImageAddress(t *testing.T) {
	imageCalled := false
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imageCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(imageServer.Close)
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	parsedUpstream, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	sidecar := httptest.NewServer(newSidecarHandler(parsedUpstream, newSecureImageClient(), []string{"127.0.0.1"}))
	t.Cleanup(sidecar.Close)
	request, err := http.NewRequest(http.MethodPost, sidecar.URL+"/v1/chat/completions", strings.NewReader(`{
		"model":"gemini-3.1-flash-image",
		"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"`+imageServer.URL+`/private.png"}}]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400", response.StatusCode)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "image_target_not_allowed" {
		t.Errorf("错误码 = %q", body.Error.Code)
	}
	if imageCalled || upstreamCalled {
		t.Fatalf("私网地址仍被访问: image=%v upstream=%v", imageCalled, upstreamCalled)
	}
}

func TestChatCompletionsRejectsRedirectOutsideAllowlist(t *testing.T) {
	pngBytes, err := base64.StdEncoding.DecodeString(onePixelPNGBase64)
	if err != nil {
		t.Fatal(err)
	}
	redirectTargetCalled := false
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start.png":
			http.Redirect(w, r, "http://other.example.com/final.png", http.StatusFound)
		case "/final.png":
			redirectTargetCalled = true
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(imageServer.Close)
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	sidecar := httptest.NewServer(newTestHandler(t, upstream.URL, imageServer, "images.example.com"))
	t.Cleanup(sidecar.Close)
	response, err := http.Post(sidecar.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{
		"model":"gemini-3.1-flash-image",
		"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"http://images.example.com/start.png"}}]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400", response.StatusCode)
	}
	if redirectTargetCalled || upstreamCalled {
		t.Fatalf("重定向绕过白名单: redirect=%v upstream=%v", redirectTargetCalled, upstreamCalled)
	}
}

func TestChatCompletionsRejectsTooManyRemoteImages(t *testing.T) {
	imageCalled := false
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imageCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(imageServer.Close)
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	parts := make([]string, 9)
	for index := range parts {
		parts[index] = `{"type":"image_url","image_url":{"url":"http://images.example.com/pixel.png"}}`
	}
	body := `{"model":"gemini-3.1-flash-image","messages":[{"role":"user","content":[` + strings.Join(parts, ",") + `]}]}`
	sidecar := httptest.NewServer(newTestHandler(t, upstream.URL, imageServer, "images.example.com"))
	t.Cleanup(sidecar.Close)
	response, err := http.Post(sidecar.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("状态码 = %d，期望 413", response.StatusCode)
	}
	if imageCalled || upstreamCalled {
		t.Fatalf("数量超限后仍发起请求: image=%v upstream=%v", imageCalled, upstreamCalled)
	}
}

func TestChatCompletionsRejectsExcessTotalImageBytes(t *testing.T) {
	pngBytes, err := base64.StdEncoding.DecodeString(onePixelPNGBase64)
	if err != nil {
		t.Fatal(err)
	}
	sevenMiB := make([]byte, 7<<20)
	copy(sevenMiB, pngBytes)
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(sevenMiB)
	}))
	t.Cleanup(imageServer.Close)
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	parts := make([]string, 3)
	for index := range parts {
		parts[index] = `{"type":"image_url","image_url":{"url":"http://images.example.com/pixel.png"}}`
	}
	body := `{"model":"gemini-3.1-flash-image","messages":[{"role":"user","content":[` + strings.Join(parts, ",") + `]}]}`
	sidecar := httptest.NewServer(newTestHandler(t, upstream.URL, imageServer, "images.example.com"))
	t.Cleanup(sidecar.Close)
	response, err := http.Post(sidecar.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("状态码 = %d，期望 413", response.StatusCode)
	}
	if upstreamCalled {
		t.Fatal("图片总字节超限后仍转发上游")
	}
}

func TestChatCompletionsRejectsOversizedRequestBody(t *testing.T) {
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("纯文本请求不应读取图片")
	}))
	t.Cleanup(imageServer.Close)
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	body := `{"model":"gemini-3.1-flash-image","messages":[{"role":"user","content":"` + strings.Repeat("x", 32<<20) + `"}]}`
	sidecar := httptest.NewServer(newTestHandler(t, upstream.URL, imageServer, "images.example.com"))
	t.Cleanup(sidecar.Close)
	response, err := http.Post(sidecar.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("状态码 = %d，期望 413", response.StatusCode)
	}
	if upstreamCalled {
		t.Fatal("超大请求体仍被转发到上游")
	}
}

func TestChatCompletionsLeavesDataURLUntouched(t *testing.T) {
	imageCalled := false
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imageCalled = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(imageServer.Close)
	body := `{"model":"gemini-3.1-flash-image","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + onePixelPNGBase64 + `"}}]}]}`
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		received, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("上游请求体读取失败: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if string(received) != body {
			t.Errorf("已有 data URL 的请求被重写:\n%s", received)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	sidecar := httptest.NewServer(newTestHandler(t, upstream.URL, imageServer, "images.example.com"))
	t.Cleanup(sidecar.Close)
	response, err := http.Post(sidecar.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !upstreamCalled || imageCalled {
		t.Fatalf("data URL 幂等失败: status=%d upstream=%v image=%v", response.StatusCode, upstreamCalled, imageCalled)
	}
}

func TestChatCompletionsStreamsBeforeUpstreamCompletes(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseUpstream := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseUpstream)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("上游不支持 Flush")
			return
		}
		_, _ = w.Write([]byte("data: first\n\n"))
		flusher.Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	t.Cleanup(upstream.Close)
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("无远程图片的请求不应下载图片")
	}))
	t.Cleanup(imageServer.Close)

	sidecar := httptest.NewServer(newTestHandler(t, upstream.URL, imageServer, "images.example.com"))
	t.Cleanup(sidecar.Close)
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Post(sidecar.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{
		"model":"gemini-3.1-flash-image",
		"stream":true,
		"messages":[{"role":"user","content":"你好"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	firstLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if firstLine != "data: first\n" {
		t.Fatalf("首个 SSE 行 = %q", firstLine)
	}
	releaseUpstream()
	remaining, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(remaining), "data: [DONE]") {
		t.Fatalf("SSE 结束帧丢失: %q", remaining)
	}
}

func TestChatCompletionsImageFailuresDoNotReachUpstream(t *testing.T) {
	pngBytes, err := base64.StdEncoding.DecodeString(onePixelPNGBase64)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name           string
		upstreamStatus int
		contentType    string
		body           string
		expectedStatus int
		expectedCode   string
	}{
		{name: "下载状态失败", upstreamStatus: http.StatusInternalServerError, contentType: "image/png", body: "error", expectedStatus: http.StatusBadGateway, expectedCode: "image_download_failed"},
		{name: "响应不是图片", upstreamStatus: http.StatusOK, contentType: "text/plain", body: "not an image", expectedStatus: http.StatusBadRequest, expectedCode: "unsupported_image_type"},
		{name: "MIME 声明与内容不一致", upstreamStatus: http.StatusOK, contentType: "image/jpeg", body: string(pngBytes), expectedStatus: http.StatusBadRequest, expectedCode: "unsupported_image_type"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				w.WriteHeader(test.upstreamStatus)
				_, _ = w.Write([]byte(test.body))
			}))
			t.Cleanup(imageServer.Close)
			upstreamCalled := false
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamCalled = true
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(upstream.Close)
			sidecar := httptest.NewServer(newTestHandler(t, upstream.URL, imageServer, "images.example.com"))
			t.Cleanup(sidecar.Close)

			response, err := http.Post(sidecar.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{
				"model":"gemini-3.1-flash-image",
				"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"http://images.example.com/image.png"}}]}]
			}`))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != test.expectedStatus {
				t.Fatalf("状态码 = %d，期望 %d", response.StatusCode, test.expectedStatus)
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != test.expectedCode || upstreamCalled {
				t.Fatalf("失败语义错误: code=%q upstream=%v", body.Error.Code, upstreamCalled)
			}
		})
	}
}

func newTestHandler(t *testing.T, upstreamURL string, imageServer *httptest.Server, allowedHosts ...string) http.Handler {
	t.Helper()
	parsedUpstream, err := url.Parse(upstreamURL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, imageServer.Listener.Addr().String())
		},
	}
	return newSidecarHandler(parsedUpstream, &http.Client{Transport: transport}, allowedHosts)
}
