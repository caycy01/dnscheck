package main

import (
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

	"gopkg.in/yaml.v3"
)

// Config 结构体，对应 YAML 文件
type Config struct {
	Domains []DomainConfig `yaml:"domains"`
}

type DomainConfig struct {
	Name         string   `yaml:"name"`
	ExpectedLlcs []string `yaml:"expected_llcs"`
}

// IPInfo API 返回的数据结构
type IPInfo struct {
	IP      string `json:"ip"`
	BeginIP string `json:"beginip"`
	EndIP   string `json:"endip"`
	Region  string `json:"region"`
	ISP     string `json:"isp"`
	ASN     string `json:"asn"`
	LLC     string `json:"llc"`
}

// 检测结果
type CheckResult struct {
	Domain     string
	IP         string
	ActualLLC  string
	Expected   []string
	IsPolluted bool  // true 表示该 IP 对应的 llc 不在预期内（或汇总后域名被污染）
	Error      error // DNS 或 API 错误
}

var (
	apiURL      = flag.String("api", "https://uapis.cn/api/v1/network/ipinfo?ip=", "IP 信息查询 API 地址")
	concurrency = flag.Int("c", 5, "并发查询数")
	strict      = flag.Bool("strict", false, "严格模式：所有解析 IP 的 llc 都必须在预期内才算正常")
	configFile  = flag.String("f", "sites.yaml", "配置文件路径")
	timeout     = flag.Duration("timeout", 10*time.Second, "HTTP 请求超时")
	outputFile  = flag.String("output", "", "输出报告文件路径（默认自动生成带时间戳的文件）")
)

func main() {
	flag.Parse()

	// 1. 加载配置
	config, err := loadConfig(*configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置文件失败: %v\n", err)
		os.Exit(1)
	}

	// 2. 创建带并发限制的工作池
	sem := make(chan struct{}, *concurrency)
	var wg sync.WaitGroup
	results := make(chan CheckResult, len(config.Domains)*2) // 缓冲避免阻塞

	// 3. 对每个域名启动 goroutine 进行检测
	for _, dc := range config.Domains {
		wg.Add(1)
		go func(dc DomainConfig) {
			defer wg.Done()
			sem <- struct{}{}        // 获取令牌
			defer func() { <-sem }() // 释放令牌

			// DNS 解析
			ips, err := net.LookupIP(dc.Name)
			if err != nil {
				results <- CheckResult{
					Domain:     dc.Name,
					Error:      fmt.Errorf("DNS 解析失败: %v", err),
					IsPolluted: true, // 解析失败视为可疑
				}
				return
			}

			// 过滤出 IPv4 地址（可选，也可保留 IPv6）
			var ipv4s []net.IP
			for _, ip := range ips {
				if ip.To4() != nil {
					ipv4s = append(ipv4s, ip)
				}
			}
			if len(ipv4s) == 0 {
				results <- CheckResult{
					Domain:     dc.Name,
					Error:      fmt.Errorf("没有找到 IPv4 地址"),
					IsPolluted: true,
				}
				return
			}

			// 对每个 IP 查询 LLC
			domainResults := make([]CheckResult, 0, len(ipv4s))
			for _, ip := range ipv4s {
				llc, err := queryLLC(ip.String(), *apiURL, *timeout)
				if err != nil {
					domainResults = append(domainResults, CheckResult{
						Domain:     dc.Name,
						IP:         ip.String(),
						Error:      err,
						IsPolluted: true,
					})
					continue
				}

				// 检查 llc 是否在预期列表中（支持前缀匹配）
				matched := false
				for _, exp := range dc.ExpectedLlcs {
					if strings.HasPrefix(llc, exp) {
						matched = true
						break
					}
				}
				domainResults = append(domainResults, CheckResult{
					Domain:     dc.Name,
					IP:         ip.String(),
					ActualLLC:  llc,
					Expected:   dc.ExpectedLlcs,
					IsPolluted: !matched,
				})
			}

			// 根据 strict 模式汇总该域名的最终结论
			// 如果 strict 为 false，只要有一个 IP 正常就算正常；否则所有 IP 必须正常
			finalPolluted := true
			if !*strict {
				// 宽松模式：至少有一个 IP 正常
				for _, r := range domainResults {
					if !r.IsPolluted && r.Error == nil {
						finalPolluted = false
						break
					}
				}
			} else {
				// 严格模式：所有 IP 都正常才算正常
				finalPolluted = false
				for _, r := range domainResults {
					if r.IsPolluted || r.Error != nil {
						finalPolluted = true
						break
					}
				}
			}

			// 将所有结果发送到通道，并附加 finalPolluted 信息
			for _, r := range domainResults {
				r.IsPolluted = finalPolluted // 将汇总结论赋值给每个子结果
				results <- r
			}
		}(dc)
	}

	// 4. 等待所有检测完成，关闭结果通道
	go func() {
		wg.Wait()
		close(results)
	}()

	// 5. 收集结果并统计
	domainMap := make(map[string][]CheckResult)
	for res := range results {
		domainMap[res.Domain] = append(domainMap[res.Domain], res)
	}

	// 统计污染域名数量
	totalDomains := len(domainMap)
	pollutedCount := 0
	for _, resList := range domainMap {
		// 取第一个结果的 IsPolluted 作为该域名的最终状态（因为所有结果该值相同）
		if len(resList) > 0 && resList[0].IsPolluted {
			pollutedCount++
		}
	}

	// 计算污染率
	pollutionRate := 0.0
	if totalDomains > 0 {
		pollutionRate = float64(pollutedCount) / float64(totalDomains) * 100
	}

	// 确定污染等级
	var level string
	switch {
	case pollutionRate < 20:
		level = "正常"
	case pollutionRate < 40:
		level = "轻度污染"
	case pollutionRate < 60:
		level = "中度污染"
	default:
		level = "重度污染"
	}

	// 生成报告内容
	report := buildReport(domainMap, totalDomains, pollutedCount, pollutionRate, level)

	// 输出到终端
	fmt.Print(report)

	// 输出到文件
	if *outputFile == "" {
		// 自动生成文件名
		*outputFile = fmt.Sprintf("dnscheck_report_%s.txt", time.Now().Format("20060102_150405"))
	}
	if err := writeReportToFile(report, *outputFile); err != nil {
		fmt.Fprintf(os.Stderr, "写入报告文件失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\n报告已保存至: %s\n", *outputFile)
}

// 加载 YAML 配置文件
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

// 调用 IP 信息 API 获取 LLC
func queryLLC(ip, baseURL string, timeout time.Duration) (string, error) {
	url := baseURL + ip
	client := http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API 返回非 200 状态码: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var info IPInfo
	err = json.Unmarshal(body, &info)
	if err != nil {
		return "", err
	}
	if info.LLC == "" {
		return "", fmt.Errorf("API 返回的 LLC 为空")
	}
	return info.LLC, nil
}

// 构建报告字符串
func buildReport(domainMap map[string][]CheckResult, total, polluted int, rate float64, level string) string {
	var b strings.Builder

	// 标题和时间
	b.WriteString("DNS 污染检测报告\n")
	b.WriteString(fmt.Sprintf("生成时间: %s\n", time.Now().Format("2006-01-02 15:04:05")))
	b.WriteString("=================\n")
	b.WriteString(fmt.Sprintf("检测域名总数: %d\n", total))
	b.WriteString(fmt.Sprintf("被污染域名数: %d\n", polluted))
	b.WriteString(fmt.Sprintf("污染率: %.2f%%\n", rate))
	b.WriteString(fmt.Sprintf("污染程度: %s\n", level))
	b.WriteString("=================\n\n")
	b.WriteString("详细结果:\n")

	for domain, resList := range domainMap {
		b.WriteString(fmt.Sprintf("域名: %s\n", domain))
		for _, r := range resList {
			if r.Error != nil {
				b.WriteString(fmt.Sprintf("  IP %s: 错误 - %v\n", r.IP, r.Error))
			} else {
				status := "正常"
				if r.IsPolluted {
					status = "可能被污染"
				}
				b.WriteString(fmt.Sprintf("  IP %s: LLC=%s (期望: %v) - %s\n", r.IP, r.ActualLLC, r.Expected, status))
			}
		}
		b.WriteString("\n")
	}

	return b.String()
}

// 将报告写入文件
func writeReportToFile(report, filename string) error {
	return os.WriteFile(filename, []byte(report), 0644)
}