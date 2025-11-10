package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/injoyai/tdx"
	"github.com/injoyai/tdx/protocol"
)

// 扩展API接口

// 获取股票代码列表
func handleGetCodes(w http.ResponseWriter, r *http.Request) {
	exchange := r.URL.Query().Get("exchange")

	type CodesResponse struct {
		Total     int                 `json:"total"`
		Exchanges map[string]int      `json:"exchanges"`
		Codes     []map[string]string `json:"codes"`
	}

	resp := &CodesResponse{
		Exchanges: map[string]int{
			"sh": 0,
			"sz": 0,
			"bj": 0,
		},
		Codes: []map[string]string{},
	}

	allCodes, err := getAllCodeModels()
	if err != nil {
		errorResponse(w, "获取代码列表失败: "+err.Error())
		return
	}
	targetExchange := strings.ToLower(exchange)

	for _, model := range allCodes {
		fullCode := model.FullCode()
		if !protocol.IsStock(fullCode) {
			continue
		}
		exName := strings.ToLower(model.Exchange)
		resp.Exchanges[exName]++

		if targetExchange != "" && targetExchange != "all" && targetExchange != exName {
			continue
		}

		resp.Codes = append(resp.Codes, map[string]string{
			"code":     model.Code,
			"name":     model.Name,
			"exchange": exName,
		})
	}

	resp.Total = len(resp.Codes)

	successResponse(w, resp)
}

// 批量获取行情
func handleBatchQuote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, "只支持POST请求")
		return
	}

	var req struct {
		Codes []string `json:"codes"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, "请求参数错误: "+err.Error())
		return
	}

	if len(req.Codes) == 0 {
		errorResponse(w, "股票代码列表不能为空")
		return
	}

	// 限制最多50只
	if len(req.Codes) > 50 {
		errorResponse(w, "一次最多查询50只股票")
		return
	}

	quotes, err := client.GetQuote(req.Codes...)
	if err != nil {
		errorResponse(w, fmt.Sprintf("获取行情失败: %v", err))
		return
	}

	successResponse(w, quotes)
}

// 获取历史K线（指定范围，日/周/月K线使用前复权）
func handleGetKlineHistory(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	klineType := r.URL.Query().Get("type")
	limitStr := r.URL.Query().Get("limit")

	if code == "" {
		errorResponse(w, "股票代码不能为空")
		return
	}

	// 解析limit，默认100，最大800
	limit := uint16(100)
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			if l > 800 {
				l = 800
			}
			limit = uint16(l)
		}
	}

	var resp *protocol.KlineResp
	var err error

	switch klineType {
	case "minute1":
		resp, err = client.GetKlineMinute(code, 0, limit)
	case "minute5":
		resp, err = client.GetKline5Minute(code, 0, limit)
	case "minute15":
		resp, err = client.GetKline15Minute(code, 0, limit)
	case "minute30":
		resp, err = client.GetKline30Minute(code, 0, limit)
	case "hour":
		resp, err = client.GetKlineHour(code, 0, limit)
	case "week":
		// 周K线使用前复权
		resp, err = getQfqKlineDay(code)
		if err == nil {
			resp = convertToWeekKline(resp)
			// 限制返回数量
			if len(resp.List) > int(limit) {
				resp.List = resp.List[len(resp.List)-int(limit):]
				resp.Count = limit
			}
		}
	case "month":
		// 月K线使用前复权
		resp, err = getQfqKlineDay(code)
		if err == nil {
			resp = convertToMonthKline(resp)
			// 限制返回数量
			if len(resp.List) > int(limit) {
				resp.List = resp.List[len(resp.List)-int(limit):]
				resp.Count = limit
			}
		}
	case "day":
		fallthrough
	default:
		// 日K线使用前复权
		resp, err = getQfqKlineDay(code)
		if err == nil && len(resp.List) > int(limit) {
			// 只返回最近limit条
			resp.List = resp.List[len(resp.List)-int(limit):]
			resp.Count = limit
		}
	}

	if err != nil {
		errorResponse(w, fmt.Sprintf("获取K线失败: %v", err))
		return
	}

	successResponse(w, resp)
}

// 获取指数数据
func handleGetIndex(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	klineType := r.URL.Query().Get("type")
	limitStr := r.URL.Query().Get("limit")

	if code == "" {
		errorResponse(w, "指数代码不能为空")
		return
	}

	// 解析limit，默认100，最大800
	limit := uint16(100)
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			if l > 800 {
				l = 800
			}
			limit = uint16(l)
		}
	}

	var resp *protocol.KlineResp
	var err error

	// 根据类型选择对应的指数接口
	switch klineType {
	case "minute1":
		resp, err = client.GetIndex(protocol.TypeKlineMinute, code, 0, limit)
	case "minute5":
		resp, err = client.GetIndex(protocol.TypeKline5Minute, code, 0, limit)
	case "minute15":
		resp, err = client.GetIndex(protocol.TypeKline15Minute, code, 0, limit)
	case "minute30":
		resp, err = client.GetIndex(protocol.TypeKline30Minute, code, 0, limit)
	case "hour":
		resp, err = client.GetIndex(protocol.TypeKline60Minute, code, 0, limit)
	case "week":
		resp, err = client.GetIndexWeekAll(code)
		if resp != nil && len(resp.List) > int(limit) {
			resp.List = resp.List[:limit]
			resp.Count = limit
		}
	case "month":
		resp, err = client.GetIndexMonthAll(code)
		if resp != nil && len(resp.List) > int(limit) {
			resp.List = resp.List[:limit]
			resp.Count = limit
		}
	case "day":
		fallthrough
	default:
		resp, err = client.GetIndexDay(code, 0, limit)
	}

	if err != nil {
		errorResponse(w, fmt.Sprintf("获取指数数据失败: %v", err))
		return
	}

	successResponse(w, resp)
}

// 获取市场统计
func handleGetMarketStats(w http.ResponseWriter, r *http.Request) {
	type MarketStats struct {
		SH struct {
			Total int `json:"total"`
			Up    int `json:"up"`
			Down  int `json:"down"`
			Flat  int `json:"flat"`
		} `json:"sh"`
		SZ struct {
			Total int `json:"total"`
			Up    int `json:"up"`
			Down  int `json:"down"`
			Flat  int `json:"flat"`
		} `json:"sz"`
		BJ struct {
			Total int `json:"total"`
			Up    int `json:"up"`
			Down  int `json:"down"`
			Flat  int `json:"flat"`
		} `json:"bj"`
		UpdateTime string `json:"update_time"`
	}

	stats := &MarketStats{}
	allCodes, err := getAllCodeModels()
	if err != nil {
		errorResponse(w, "获取市场统计失败: "+err.Error())
		return
	}

	for _, model := range allCodes {
		fullCode := model.FullCode()
		if !protocol.IsStock(fullCode) {
			continue
		}
		lastPrice := model.LastPrice
		switch strings.ToLower(model.Exchange) {
		case "sh":
			stats.SH.Total++
			classifyPrice(lastPrice, &stats.SH.Up, &stats.SH.Down, &stats.SH.Flat)
		case "sz":
			stats.SZ.Total++
			classifyPrice(lastPrice, &stats.SZ.Up, &stats.SZ.Down, &stats.SZ.Flat)
		case "bj":
			stats.BJ.Total++
			classifyPrice(lastPrice, &stats.BJ.Up, &stats.BJ.Down, &stats.BJ.Flat)
		}
	}

	successResponse(w, stats)
}

// 获取服务器状态
func handleGetServerStatus(w http.ResponseWriter, r *http.Request) {
	type ServerStatus struct {
		Status    string `json:"status"`
		Connected bool   `json:"connected"`
		Version   string `json:"version"`
		Uptime    string `json:"uptime"`
	}

	status := &ServerStatus{
		Status:    "running",
		Connected: true,
		Version:   "1.0.0",
		Uptime:    "unknown",
	}

	successResponse(w, status)
}

// 健康检查
func handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "healthy",
		"time":   fmt.Sprintf("%d", 1730617200),
	})
}

func getAllCodeModels() ([]*tdx.CodeModel, error) {
	if tdx.DefaultCodes != nil {
		if list, err := tdx.DefaultCodes.GetCodes(true); err == nil && len(list) > 0 {
			return list, nil
		} else if err != nil {
			log.Printf("从数据库读取代码失败: %v", err)
		}
	}

	aggregate := []*tdx.CodeModel{}
	for _, ex := range []protocol.Exchange{protocol.ExchangeSH, protocol.ExchangeSZ, protocol.ExchangeBJ} {
		resp, err := client.GetCodeAll(ex)
		if err != nil || resp == nil {
			if err != nil {
				log.Printf("从服务器获取代码失败(%s): %v", ex.String(), err)
			}
			continue
		}
		for _, v := range resp.List {
			aggregate = append(aggregate, &tdx.CodeModel{
				Name:      v.Name,
				Code:      v.Code,
				Exchange:  ex.String(),
				Multiple:  v.Multiple,
				Decimal:   v.Decimal,
				LastPrice: v.LastPrice,
			})
		}
	}

	return aggregate, nil
}

func classifyPrice(price float64, up, down, flat *int) {
	switch {
	case price > 0:
		*up++
	case price < 0:
		*down++
	default:
		*flat++
	}
}
