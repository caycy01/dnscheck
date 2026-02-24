# DNS 污染检测工具

一款跨平台的 Go 语言工具，用于检测当前网络环境下的 DNS 解析是否被污染。通过对比域名解析 IP 的归属方（LLC）与预期值，判断解析结果是否异常，并生成统计报告。

## 功能特性

- 支持 Windows、Linux、macOS 全平台运行
- 使用系统默认 DNS 进行解析，真实反映当前网络配置
- 可配置的域名列表（YAML 格式），每个域名可指定多个预期 LLC（支持前缀匹配）
- 调用 IP 归属 API 查询每个 IP 的 LLC 信息
- 并发查询，提高检测效率
- 自动统计：
  - 检测域名总数
  - 被污染域名数
  - 污染百分比
  - 污染等级（正常 / 轻度 / 中度 / 重度）
- 输出终端报告的同时，自动生成带时间戳的报告文件（可自定义路径）
- 灵活的匹配模式（严格 / 宽松）

## 安装与编译

### 环境要求
- Go 1.16 或更高版本

### 获取代码
```bash
git clone https://github.com/yourusername/dnscheck.git
cd dnscheck
```

### 安装依赖
```bash
go mod init dnscheck
go get gopkg.in/yaml.v3
```

### 编译
**Linux / macOS:**
```bash
go build -o dnscheck main.go
```

**Windows:**
```bash
go build -o dnscheck.exe main.go
```

## 配置文件

程序默认读取同目录下的 `sites.yaml` 文件，格式如下：

```yaml
domains:
  - name: www.facebook.com
    expected_llcs: ["FACEBOOK"]
  - name: www.google.com
    expected_llcs: ["GOOGLE"]
  - name: www.gmail.com
    expected_llcs: ["GOOGLE"]
  - name: amazon.co.jp
    expected_llcs: ["AMAZON"]
  - name: www.apkmirror.com
    expected_llcs: ["CLOUDFLARE"]
  - name: archiveofourown.org
    expected_llcs: ["CLOUDFLARE"]
  - name: audiomack.com
    expected_llcs: ["AMAZON"]
  - name: bbc.com
    expected_llcs: ["FASTLY"]
  - name: www.dailymotion.com
    expected_llcs: ["GOOGLE", "AMAZON"]   # 多个预期值
  - name: discord.com
    expected_llcs: ["CLOUDFLARE"]
  - name: www.dlsite.com
    expected_llcs: ["CLOUDFLARE"]
```

**说明：**
- `name`：待检测的域名
- `expected_llcs`：该域名预期归属的 LLC 列表，支持前缀匹配（如 `AMAZON` 可匹配 `AMAZON-01`、`AMAZON-02` 等）

## 使用方法

### 基本运行
```bash
./dnscheck
```

### 命令行参数
| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-f` | `sites.yaml` | 配置文件路径 |
| `-api` | `https://uapis.cn/api/v1/network/ipinfo?ip=` | IP 归属查询 API 地址 |
| `-c` | `5` | 并发查询数 |
| `-strict` | `false` | 严格模式（true：所有解析 IP 的 LLC 均需符合预期才算正常） |
| `-timeout` | `10s` | HTTP 请求超时时间 |
| `-output` | 自动生成 | 报告输出文件路径，如 `-output report.txt` |

### 示例
```bash
# 使用自定义配置文件，严格模式，并发 10
./dnscheck -f my_sites.yaml -strict -c 10 -output my_report.txt
```

## 输出说明

程序运行后，终端会显示类似以下内容的报告，同时自动保存到文件：

```
DNS 污染检测报告
生成时间: 2025-03-30 15:30:45
=================
检测域名总数: 10
被污染域名数: 9
污染率: 90.00%
污染程度: 重度污染
=================

详细结果:
域名: www.facebook.com
  IP 157.240.22.35: LLC=FACEBOOK (期望: [FACEBOOK]) - 正常
域名: www.google.com
  IP 142.250.185.100: LLC=GOOGLE (期望: [GOOGLE]) - 正常
域名: amazon.co.jp
  IP 54.240.196.165: LLC=AMAZON-02 (期望: [AMAZON]) - 正常
域名: www.apkmirror.com
  IP 104.17.124.14: LLC=CLOUDFLARE (期望: [CLOUDFLARE]) - 正常
...
```

### 污染等级划分
- **污染率 < 20%**：正常
- **20% ≤ 污染率 < 40%**：轻度污染
- **40% ≤ 污染率 < 60%**：中度污染
- **污染率 ≥ 60%**：重度污染

## 注意事项

1. **API 依赖**：程序需要调用外部 IP 归属 API，请确保网络可达且 API 未限流。如 API 不可用，可替换为其他提供类似 JSON 格式的 API。
2. **DNS 解析**：使用系统默认 DNS，若系统本身已配置被污染的 DNS，则解析结果可能异常，但本程序正是通过对比来发现这种异常。
4. **IPv6 支持**：程序默认只检查 IPv4 地址，如需 IPv6 可修改代码中的过滤逻辑。

