package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"gopkg.in/yaml.v3"
)

// ---------- 配置结构 ----------
type Config struct {
	Domains []DomainConfig `yaml:"domains"`
}

type DomainConfig struct {
	Name         string   `yaml:"name"`
	ExpectedLlcs []string `yaml:"expected_llcs"` // 支持前缀匹配
}

// ---------- API 响应 ----------
// IPInfoRaw 使用 map 灵活解析，避免字段变更导致崩溃
type IPInfoRaw map[string]interface{}

// ---------- 检测结果 ----------
type IPCheckResult struct {
	IP        string
	ActualLLC string
	Error     error // 若为 nil 表示查询成功
}

type DomainResult struct {
	Domain      string
	Expected    []string
	IPResults   []IPCheckResult
	IsPolluted  bool // 汇总后的污染结论
	Summary     string
}

// ---------- 命令行参数 ----------
var (
	apiURL      = flag.String("api", "https://uapis.cn/api/v1/network/ipinfo?ip=", "IP 信息查询 API 地址（支持多个，用逗号分隔，将依次尝试）")
	concurrency = flag.Int("c", 2, "并发查询数")
	strict      = flag.Bool("strict", false, "严格模式：所有解析 IP 的 llc 都必须在预期内才算正常")
	configFile  = flag.String("f", "sites.yaml", "配置文件路径")
	timeout     = flag.Duration("timeout", 10*time.Second, "HTTP 请求超时")
	outputFile  = flag.String("output", "", "输出报告文件路径（默认自动生成带时间戳的文件）")
	rps         = flag.Float64("rps", 2, "每秒请求数限制 (0 表示不限速)")
	maxRetries  = flag.Int("retry", 2, "API 请求失败时的最大重试次数")
)

func main() {
	flag.Parse()

	// 1. 加载配置
	config, err := loadConfig(*configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置文件失败: %v\n", err)
		os.Exit(1)
	}

	// 2. 创建速率限制器
	var limiter *rate.Limiter
	if *rps > 0 {
		limiter = rate.NewLimiter(rate.Limit(*rps), 1)
	}

	// 3. 解析 API 列表（支持多个备用）
	apiList := strings.Split(*apiURL, ",")
	for i := range apiList {
		apiList[i] = strings.TrimSpace(apiList[i])
	}

	// 4. 带并发限制的工作池
	sem := make(chan struct{}, *concurrency)
	var wg sync.WaitGroup
	results := make(chan DomainResult, len(config.Domains))

	// 5. 处理每个域名
	for _, dc := range config.Domains {
		wg.Add(1)
		go func(dc DomainConfig) {
			defer wg.Done()
			sem <- struct{}{}        // 获取令牌
			defer func() { <-sem }() // 释放令牌

			// DNS 解析（带超时）
			ctx, cancel := context.WithTimeout(context.Background(), *timeout)
			defer cancel()
			var r net.Resolver
			ips, err := r.LookupIP(ctx, "ip4", dc.Name) // 直接获取 IPv4
			if err != nil {
				results <- DomainResult{
					Domain:     dc.Name,
					Expected:   dc.ExpectedLlcs,
					Summary:    fmt.Sprintf("DNS 解析失败: %v", err),
					IsPolluted: true, // 解析失败视为可疑
				}
				return
			}
			if len(ips) == 0 {
				results <- DomainResult{
					Domain:     dc.Name,
					Expected:   dc.ExpectedLlcs,
					Summary:    "没有找到 IPv4 地址",
					IsPolluted: true,
				}
				return
			}

			// 对每个 IP 查询 LLC
			ipResults := make([]IPCheckResult, 0, len(ips))
			for _, ip := range ips {
				// 速率限制等待
				if limiter != nil {
					_ = limiter.Wait(context.Background()) // 忽略错误，因为不会发生
				}

				llc, err := fetchLLCWithRetry(ip.String(), apiList, *timeout, *maxRetries)
				ipResults = append(ipResults, IPCheckResult{
					IP:        ip.String(),
					ActualLLC: llc,
					Error:     err,
				})
			}

			// 汇总该域名的结论
			domainRes := aggregateDomainResult(dc.Name, dc.ExpectedLlcs, ipResults, *strict)
			results <- domainRes
		}(dc)
	}

	// 6. 等待所有任务完成，关闭结果通道
	go func() {
		wg.Wait()
		close(results)
	}()

	// 7. 收集结果并生成报告
	var domainResults []DomainResult
	for res := range results {
		domainResults = append(domainResults, res)
	}

	// 8. 统计与报告
	report := buildReport(domainResults)
	fmt.Print(report)

	if *outputFile == "" {
		*outputFile = fmt.Sprintf("dnscheck_report_%s.txt", time.Now().Format("20060102_150405"))
	}
	if err := writeReportToFile(report, *outputFile); err != nil {
		fmt.Fprintf(os.Stderr, "写入报告文件失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\n报告已保存至: %s\n", *outputFile)
}

// ---------- 加载配置 ----------
func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	err = yaml.Unmarshal(data, &cfg)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ---------- 带重试的 LLC 查询 ----------
func fetchLLCWithRetry(ip string, apiList []string, timeout time.Duration, maxRetries int) (string, error) {
	var lastErr error
	// 对每个 API 端点依次尝试
	for _, baseURL := range apiList {
		for attempt := 0; attempt <= maxRetries; attempt++ {
			llc, err := queryLLCFromAPI(ip, baseURL, timeout)
			if err == nil {
				return llc, nil
			}
			lastErr = err
			// 如果是可重试的错误（如网络超时、5xx），则等待后重试
			if isRetryable(err) && attempt < maxRetries {
				time.Sleep(backoffDuration(attempt))
				continue
			}
			// 否则跳出当前 API 的重试循环，尝试下一个 API
			break
		}
	}
	return "", fmt.Errorf("所有 API 尝试均失败: %w", lastErr)
}

// 判断错误是否可重试（可根据需要扩展）
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	// 简单示例：网络超时、临时性错误可重试
	if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "EOF") {
		return true
	}
	// 可增加对具体 HTTP 状态码的判断（由上层传入）
	return false
}

// 退避时间：指数退避
func backoffDuration(attempt int) time.Duration {
	return time.Duration(1<<uint(attempt)) * time.Second
}

// ---------- 调用单个 API 获取 LLC ----------
func queryLLCFromAPI(ip, baseURL string, timeout time.Duration) (string, error) {
	url := baseURL + ip
	client := http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("HTTP 请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// 将 4xx 视为不可重试，5xx 视为可重试（由上层决定）
		return "", fmt.Errorf("API 返回非 200 状态码: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应体失败: %w", err)
	}

	// 使用 map 解析，避免字段变更导致崩溃
	var raw IPInfoRaw
	err = json.Unmarshal(body, &raw)
	if err != nil {
		return "", fmt.Errorf("JSON 解析失败: %w", err)
	}

	// 提取 llc 字段，支持多种可能的键名（可配置）
	llc, err := extractLLC(raw)
	if err != nil {
		return "", err
	}
	return llc, nil
}

// 从解析后的 map 中提取 LLC 字段（容错处理）
func extractLLC(data map[string]interface{}) (string, error) {
	// 尝试常见的字段名
	possibleKeys := []string{"llc", "isp", "carrier", "org", "asn_description"}
	for _, key := range possibleKeys {
		if val, ok := data[key]; ok {
			if str, ok := val.(string); ok && str != "" {
				return str, nil
			}
		}
	}
	// 如果都没有，返回错误，但附带部分数据供调试
	return "", fmt.Errorf("无法从响应中提取 LLC 字段，响应内容: %v", data)
}

// ---------- 汇总域名结果 ----------
func aggregateDomainResult(domain string, expected []string, ipResults []IPCheckResult, strict bool) DomainResult {
	// 先统计每个 IP 是否匹配预期
	ipMatches := make([]bool, len(ipResults))
	anySuccess := false
	allMatch := true

	for i, res := range ipResults {
		if res.Error != nil {
			// 查询失败的 IP 视为不匹配
			ipMatches[i] = false
			allMatch = false
			continue
		}
		// 检查 LLC 是否匹配预期（前缀匹配）
		matched := false
		for _, exp := range expected {
			if strings.HasPrefix(res.ActualLLC, exp) {
				matched = true
				break
			}
		}
		ipMatches[i] = matched
		if matched {
			anySuccess = true
		} else {
			allMatch = false
		}
	}

	// 根据模式确定最终污染结论
	var polluted bool
	var summary string
	if strict {
		polluted = !allMatch // 严格模式：必须全部匹配才算正常
		if polluted {
			summary = "严格模式：部分 IP 不符合预期"
		} else {
			summary = "所有 IP 均符合预期"
		}
	} else {
		polluted = !anySuccess // 宽松模式：至少有一个匹配才算正常
		if polluted {
			summary = "宽松模式：无任何 IP 符合预期"
		} else {
			summary = "至少有一个 IP 符合预期"
		}
	}

	// 构建详细 IP 结果列表（保持原样）
	detailed := make([]IPCheckResult, len(ipResults))
	copy(detailed, ipResults)

	return DomainResult{
		Domain:     domain,
		Expected:   expected,
		IPResults:  detailed,
		IsPolluted: polluted,
		Summary:    summary,
	}
}

// ---------- 构建报告 ----------
func buildReport(results []DomainResult) string {
	var b strings.Builder

	// 统计
	total := len(results)
	polluted := 0
	for _, r := range results {
		if r.IsPolluted {
			polluted++
		}
	}
	rate := 0.0
	if total > 0 {
		rate = float64(polluted) / float64(total) * 100
	}
	level := pollutionLevel(rate)

	b.WriteString("DNS 污染检测报告\n")
	b.WriteString(fmt.Sprintf("生成时间: %s\n", time.Now().Format("2006-01-02 15:04:05")))
	b.WriteString("=================\n")
	b.WriteString(fmt.Sprintf("检测域名总数: %d\n", total))
	b.WriteString(fmt.Sprintf("被污染域名数: %d\n", polluted))
	b.WriteString(fmt.Sprintf("污染率: %.2f%%\n", rate))
	b.WriteString(fmt.Sprintf("污染程度: %s\n", level))
	b.WriteString("=================\n\n")
	b.WriteString("详细结果:\n")

	for _, res := range results {
		b.WriteString(fmt.Sprintf("域名: %s\n", res.Domain))
		b.WriteString(fmt.Sprintf("  汇总: %s (污染: %v)\n", res.Summary, res.IsPolluted))
		for _, ipRes := range res.IPResults {
			if ipRes.Error != nil {
				b.WriteString(fmt.Sprintf("  IP %s: 错误 - %v\n", ipRes.IP, ipRes.Error))
			} else {
				// 检查是否匹配预期（用于报告显示）
				matched := false
				for _, exp := range res.Expected {
					if strings.HasPrefix(ipRes.ActualLLC, exp) {
						matched = true
						break
					}
				}
				status := "正常"
				if !matched {
					status = "可能被污染"
				}
				b.WriteString(fmt.Sprintf("  IP %s: LLC=%s (期望: %v) - %s\n", ipRes.IP, ipRes.ActualLLC, res.Expected, status))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func pollutionLevel(rate float64) string {
	switch {
	case rate < 20:
		return "正常"
	case rate < 40:
		return "轻度污染"
	case rate < 60:
		return "中度污染"
	default:
		return "重度污染"
	}
}

// ---------- 写入文件 ----------
func writeReportToFile(report, filename string) error {
	return os.WriteFile(filename, []byte(report), 0644)
}
