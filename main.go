package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func main() {
	listenAddress := environmentOrDefault("LISTEN_ADDR", ":8080")
	upstream, err := parseUpstream(environmentOrDefault("CLIPROXYAPI_URL", "http://cli-proxy-api:8317"))
	if err != nil {
		log.Fatal(err)
	}
	allowedHosts := parseAllowedHosts(os.Getenv("IMAGE_HOST_ALLOWLIST"))
	if len(allowedHosts) == 0 {
		log.Fatal("IMAGE_HOST_ALLOWLIST 至少需要一个精确图片域名")
	}

	server := &http.Server{
		Addr:              listenAddress,
		Handler:           newSidecarHandler(upstream, newSecureImageClient(), allowedHosts),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	log.Printf("image-sidecar 开始监听 %s", listenAddress)
	log.Fatal(server.ListenAndServe())
}

func environmentOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func parseUpstream(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("CLIPROXYAPI_URL 无效: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("CLIPROXYAPI_URL 只能是不带路径、查询或用户信息的 HTTP/HTTPS 根地址")
	}
	return parsed, nil
}

func parseAllowedHosts(value string) []string {
	entries := strings.Split(value, ",")
	hosts := make([]string, 0, len(entries))
	for _, entry := range entries {
		if host := normalizeHost(entry); host != "" {
			hosts = append(hosts, host)
		}
	}
	return hosts
}
