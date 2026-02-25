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

### 编译命令
```bash
go mod init dnscheck  # 如果未初始化模块
go get golang.org/x/time/rate gopkg.in/yaml.v3
go build -ldflags="-s -w" -trimpath -o dnscheck
```

编译后得到单文件 `dnscheck`，可直接运行。

> 若要进一步压缩体积，可使用 UPX：`upx --best dnscheck`

---


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
## 命令行参数

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `-api` | string | `https://uapis.cn/api/v1/network/ipinfo?ip=` | IP 信息查询 API 地址（支持多个，用逗号分隔） |
| `-c` | int | `2` | 并发查询的域名数 |
| `-strict` | bool | `false` | 严格模式（所有 IP 必须匹配） |
| `-f` | string | `sites.yaml` | 配置文件路径（默认使用内嵌配置） |
| `-timeout` | duration | `10s` | HTTP 请求超时时间 |
| `-output` | string | 自动生成 | 输出报告文件路径，若不指定则自动生成带时间戳的文件 |
| `-rps` | float | `2` | 每秒 API 请求数限制（0 表示不限速） |
| `-retry` | int | `2` | API 请求失败时的最大重试次数 |

---

## 使用示例

### 基本使用
```bash
./dnscheck
```
使用默认配置文件 `sites.yaml`（若无则使用内嵌默认），并发 2，API 请求限速 2 rps。

### 指定配置文件
```bash
./dnscheck -f my_sites.yaml
```

### 使用多个备用 API
```bash
./dnscheck -api="https://api1.example.com/ip?ip=,https://api2.example.com/ip?ip="
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

1. **API 兼容性**：默认 API 返回的 JSON 中应包含 `llc` 字段。若字段名不同，可修改 `extractLLC` 函数中的 `possibleKeys` 列表。
2. **配置文件嵌入**：使用 `//go:embed` 嵌入的默认配置文件必须与 `main.go` 位于同一目录，且文件名为 `sites.yaml`。
3. **并发与速率限制**：`-c` 控制域名级并发，`-rps` 控制全局 API 请求速率。建议根据 API 限制合理调整。

---
