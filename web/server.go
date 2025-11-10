package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/injoyai/tdx"
	"github.com/injoyai/tdx/extend"
	"github.com/injoyai/tdx/protocol"
)

var client *tdx.Client

func init() {
	var err error
	// 连接通达信服务器
	client, err = tdx.DialDefault(tdx.WithDebug(false))
	if err != nil {
		log.Fatalf("连接服务器失败: %v", err)
	}
	log.Println("成功连接到通达信服务器")

	// 初始化代码缓存
	if err = os.MkdirAll(tdx.DefaultDatabaseDir, 0755); err != nil {
		log.Printf("创建数据目录失败: %v", err)
	}
	if codes, err := tdx.NewCodesSqlite(client); err != nil {
		log.Printf("初始化代码库失败: %v", err)
	} else {
		tdx.DefaultCodes = codes
		if err := tdx.DefaultCodes.Update(); err != nil {
			log.Printf("更新代码库失败: %v", err)
		} else {
			log.Printf("已加载股票代码，共 %d 条", len(tdx.DefaultCodes.Map))
		}
	}
}

// Response 统一响应结构
type Response struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

// 返回成功响应
func successResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(Response{
		Code:    0,
		Message: "success",
		Data:    data,
	})
}

// 返回错误响应
func errorResponse(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(Response{
		Code:    -1,
		Message: message,
		Data:    nil,
	})
}

// 获取五档行情
func handleGetQuote(w http.ResponseWriter, r *http.Request) {
	codeParam := r.URL.Query().Get("code")
	if codeParam == "" {
		errorResponse(w, "股票代码不能为空")
		return
	}

	codes := splitCodes(codeParam)
	if len(codes) == 0 {
		errorResponse(w, "股票代码不能为空")
		return
	}

	quotes, err := client.GetQuote(codes...)
	if err != nil {
		errorResponse(w, fmt.Sprintf("获取行情失败: %v", err))
		return
	}

	successResponse(w, quotes)
}

// 获取K线数据（日K线默认使用前复权）
func handleGetKline(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	klineType := r.URL.Query().Get("type") // minute1/minute5/minute15/minute30/hour/day/week/month
	if code == "" {
		errorResponse(w, "股票代码不能为空")
		return
	}

	var resp *protocol.KlineResp
	var err error

	switch klineType {
	case "minute1":
		// 分钟K线不需要复权
		resp, err = client.GetKlineMinuteAll(code)
	case "minute5":
		resp, err = client.GetKline5MinuteAll(code)
	case "minute15":
		resp, err = client.GetKline15MinuteAll(code)
	case "minute30":
		resp, err = client.GetKline30MinuteAll(code)
	case "hour":
		resp, err = client.GetKlineHourAll(code)
	case "week":
		// 周K线使用前复权（从日K线转换）
		resp, err = getQfqKlineDay(code)
		if err == nil && len(resp.List) > 0 {
			// 将日K线转换为周K线（简化版：每5个交易日合并）
			resp = convertToWeekKline(resp)
		}
	case "month":
		// 月K线使用前复权（从日K线转换）
		resp, err = getQfqKlineDay(code)
		if err == nil && len(resp.List) > 0 {
			// 将日K线转换为月K线
			resp = convertToMonthKline(resp)
		}
	case "day":
		fallthrough
	default:
		// 日K线使用前复权数据
		resp, err = getQfqKlineDay(code)
	}

	if err != nil {
		errorResponse(w, fmt.Sprintf("获取K线失败: %v", err))
		return
	}

	successResponse(w, resp)
}

// getQfqKlineDay 获取前复权日K线数据
func getQfqKlineDay(code string) (*protocol.KlineResp, error) {
	// 使用同花顺API获取前复权数据
	klines, err := extend.GetTHSDayKline(code, extend.THS_QFQ)
	if err != nil {
		// 如果同花顺API失败，降级使用通达信不复权数据
		log.Printf("获取前复权数据失败，使用不复权数据: %v", err)
		return client.GetKlineDay(code, 0, 800)
	}

	// 转换为 protocol.KlineResp 格式
	resp := &protocol.KlineResp{
		Count: uint16(len(klines)),
		List:  make([]*protocol.Kline, 0, len(klines)),
	}

	for i, k := range klines {
		pk := &protocol.Kline{
			Time:   time.Unix(k.Date, 0),
			Open:   k.Open,
			High:   k.High,
			Low:    k.Low,
			Close:  k.Close,
			Volume: k.Volume,
			Amount: k.Amount,
		}
		// 设置昨收价（使用上一条K线的收盘价）
		if i > 0 {
			pk.Last = klines[i-1].Close
		}
		resp.List = append(resp.List, pk)
	}

	return resp, nil
}

// convertToWeekKline 将日K线转换为周K线（简化版）
func convertToWeekKline(dayKline *protocol.KlineResp) *protocol.KlineResp {
	if len(dayKline.List) == 0 {
		return dayKline
	}

	weekResp := &protocol.KlineResp{
		List: make([]*protocol.Kline, 0),
	}

	var currentWeek *protocol.Kline
	var lastWeekDay time.Time

	for _, k := range dayKline.List {
		year, week := k.Time.ISOWeek()

		// 判断是否是新的一周
		if currentWeek == nil || lastWeekDay.Year() != year || getISOWeek(lastWeekDay) != week {
			// 保存上一周的数据
			if currentWeek != nil {
				weekResp.List = append(weekResp.List, currentWeek)
			}
			// 创建新周
			currentWeek = &protocol.Kline{
				Time:   k.Time,
				Last:   k.Last,
				Open:   k.Open,
				High:   k.High,
				Low:    k.Low,
				Close:  k.Close,
				Volume: k.Volume,
				Amount: k.Amount,
			}
		} else {
			// 累积当周数据
			if k.High > currentWeek.High {
				currentWeek.High = k.High
			}
			if k.Low < currentWeek.Low || currentWeek.Low == 0 {
				currentWeek.Low = k.Low
			}
			currentWeek.Close = k.Close
			currentWeek.Volume += k.Volume
			currentWeek.Amount += k.Amount
			currentWeek.Time = k.Time // 使用最后一天的时间
		}
		lastWeekDay = k.Time
	}

	// 添加最后一周
	if currentWeek != nil {
		weekResp.List = append(weekResp.List, currentWeek)
	}

	weekResp.Count = uint16(len(weekResp.List))
	return weekResp
}

// convertToMonthKline 将日K线转换为月K线
func convertToMonthKline(dayKline *protocol.KlineResp) *protocol.KlineResp {
	if len(dayKline.List) == 0 {
		return dayKline
	}

	monthResp := &protocol.KlineResp{
		List: make([]*protocol.Kline, 0),
	}

	var currentMonth *protocol.Kline
	var lastMonthKey string

	for _, k := range dayKline.List {
		monthKey := k.Time.Format("200601") // YYYYMM

		// 判断是否是新的一月
		if currentMonth == nil || lastMonthKey != monthKey {
			// 保存上一月的数据
			if currentMonth != nil {
				monthResp.List = append(monthResp.List, currentMonth)
			}
			// 创建新月
			currentMonth = &protocol.Kline{
				Time:   k.Time,
				Last:   k.Last,
				Open:   k.Open,
				High:   k.High,
				Low:    k.Low,
				Close:  k.Close,
				Volume: k.Volume,
				Amount: k.Amount,
			}
		} else {
			// 累积当月数据
			if k.High > currentMonth.High {
				currentMonth.High = k.High
			}
			if k.Low < currentMonth.Low || currentMonth.Low == 0 {
				currentMonth.Low = k.Low
			}
			currentMonth.Close = k.Close
			currentMonth.Volume += k.Volume
			currentMonth.Amount += k.Amount
			currentMonth.Time = k.Time // 使用最后一天的时间
		}
		lastMonthKey = monthKey
	}

	// 添加最后一月
	if currentMonth != nil {
		monthResp.List = append(monthResp.List, currentMonth)
	}

	monthResp.Count = uint16(len(monthResp.List))
	return monthResp
}

// getISOWeek 获取ISO周数
func getISOWeek(t time.Time) int {
	_, week := t.ISOWeek()
	return week
}

// 获取分时数据
func handleGetMinute(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	date := r.URL.Query().Get("date")
	if code == "" {
		errorResponse(w, "股票代码不能为空")
		return
	}

	resp, usedDate, err := getMinuteWithFallback(code, date)
	if err != nil {
		errorResponse(w, fmt.Sprintf("获取分时数据失败: %v", err))
		return
	}

	if resp == nil {
		successResponse(w, map[string]interface{}{
			"date":  usedDate,
			"Count": 0,
			"List":  []interface{}{},
		})
		return
	}

	successResponse(w, map[string]interface{}{
		"date":  usedDate,
		"Count": resp.Count,
		"List":  resp.List,
	})
}

// 获取分时成交
func handleGetTrade(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	date := r.URL.Query().Get("date")
	if code == "" {
		errorResponse(w, "股票代码不能为空")
		return
	}

	var resp *protocol.TradeResp
	var err error

	if date == "" {
		// 获取今日分时成交（最近1800条）
		resp, err = client.GetMinuteTrade(code, 0, 1800)
	} else {
		// 获取历史某天的分时成交
		resp, err = client.GetHistoryMinuteTradeDay(date, code)
	}

	if err != nil {
		errorResponse(w, fmt.Sprintf("获取分时成交失败: %v", err))
		return
	}

	successResponse(w, resp)
}

// 搜索股票代码
func handleSearchCode(w http.ResponseWriter, r *http.Request) {
	keyword := r.URL.Query().Get("keyword")
	if keyword == "" {
		errorResponse(w, "搜索关键词不能为空")
		return
	}

	keywordUpper := strings.ToUpper(keyword)
	results := []map[string]string{}
	seen := map[string]struct{}{}

	codeModels, err := getAllCodeModels()
	if err != nil {
		errorResponse(w, "搜索失败: "+err.Error())
		return
	}

	for _, model := range codeModels {
		fullCode := model.FullCode()
		if !protocol.IsStock(fullCode) {
			continue
		}
		if _, ok := seen[model.Code]; ok {
			continue
		}

		codeUpper := strings.ToUpper(model.Code)
		nameUpper := strings.ToUpper(model.Name)
		if strings.Contains(codeUpper, keywordUpper) || strings.Contains(nameUpper, keywordUpper) {
			results = append(results, map[string]string{
				"code":     model.Code,
				"name":     model.Name,
				"exchange": strings.ToLower(model.Exchange),
			})
			seen[model.Code] = struct{}{}
		}

		if len(results) >= 50 {
			break
		}
	}

	successResponse(w, results)
}

// 获取股票基本信息（整合多个接口）
func handleGetStockInfo(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		errorResponse(w, "股票代码不能为空")
		return
	}

	// 整合多个数据源
	result := make(map[string]interface{})

	// 1. 获取五档行情
	quotes, err := client.GetQuote(code)
	if err == nil && len(quotes) > 0 {
		result["quote"] = quotes[0]
	}

	// 2. 获取最近30天的日K线（使用前复权）
	kline, err := getQfqKlineDay(code)
	if err == nil && len(kline.List) > 30 {
		// 只返回最近30条
		kline.List = kline.List[len(kline.List)-30:]
		kline.Count = 30
	}
	if err == nil {
		result["kline_day"] = kline
	}

	// 3. 获取今日分时数据
	minute, minuteDate, err := getMinuteWithFallback(code, "")
	if err == nil && minute != nil {
		result["minute"] = map[string]interface{}{
			"date":  minuteDate,
			"Count": minute.Count,
			"List":  minute.List,
		}
	}

	successResponse(w, result)
}

func splitCodes(param string) []string {
	parts := strings.Split(param, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		code := strings.TrimSpace(p)
		if code != "" {
			result = append(result, code)
		}
	}
	return result
}

func getMinuteWithFallback(code, date string) (*protocol.MinuteResp, string, error) {
	if date != "" {
		resp, err := client.GetHistoryMinute(date, code)
		return resp, date, err
	}

	today := time.Now()
	const maxLookback = 10

	var lastResp *protocol.MinuteResp
	var lastDate string
	var lastErr error

	for i := 0; i < maxLookback; i++ {
		currentDate := today.AddDate(0, 0, -i).Format("20060102")
		resp, err := client.GetHistoryMinute(currentDate, code)
		if err != nil {
			lastErr = err
			continue
		}
		if resp != nil {
			if len(resp.List) > 0 && resp.Count > 0 {
				return resp, currentDate, nil
			}
			if lastResp == nil {
				lastResp = resp
				lastDate = currentDate
			}
		}
	}

	if lastResp != nil {
		return lastResp, lastDate, nil
	}

	return nil, "", lastErr
}

func main() {
	// 静态文件服务
	http.Handle("/", http.FileServer(http.Dir("./static")))

	// API路由
	http.HandleFunc("/api/quote", handleGetQuote)
	http.HandleFunc("/api/kline", handleGetKline)
	http.HandleFunc("/api/minute", handleGetMinute)
	http.HandleFunc("/api/trade", handleGetTrade)
	http.HandleFunc("/api/search", handleSearchCode)
	http.HandleFunc("/api/stock-info", handleGetStockInfo)
	http.HandleFunc("/api/codes", handleGetCodes)
	http.HandleFunc("/api/batch-quote", handleBatchQuote)
	http.HandleFunc("/api/kline-history", handleGetKlineHistory)
	http.HandleFunc("/api/index", handleGetIndex)
	http.HandleFunc("/api/market-stats", handleGetMarketStats)
	http.HandleFunc("/api/server-status", handleGetServerStatus)
	http.HandleFunc("/api/health", handleHealthCheck)

	port := ":8080"
	log.Printf("服务启动成功，访问 http://localhost%s\n", port)
	log.Fatal(http.ListenAndServe(port, nil))
}
