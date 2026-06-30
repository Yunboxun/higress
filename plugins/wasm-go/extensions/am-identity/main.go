package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
)

const ConsumerKey = "x-mse-consumer"

func main() {}

var (
	cacheMap = make(map[string]*AppInfo)
)

func init() {
	wrapper.SetCtx(
		"am-identity",
		wrapper.ParseConfigBy(parseConfig),
		wrapper.ProcessRequestHeadersBy(onHttpRequestHeaders),
	)
}

type AppInfo struct {
	AppID      string   `json:"app_id"`
	AppName    string   `json:"app_name"`
	DeptID     string   `json:"dept_id"`
	DeptName   string   `json:"dept_name"`
	QID        string   `json:"qid"`
	TenantID   string   `json:"tenant_id"`
	TenantName string   `json:"tenant_name"`
	ZGroupID   string   `json:"zgroup_id"`
	ZGroupName string   `json:"zgroup_name"`
	APIKeys    []APIKey `json:"api_key"`
}

type APIKey struct {
	APIKey string `json:"apikey"`
	ID     int    `json:"id"`
}

type APIResponse struct {
	Errno     int     `json:"errno"`
	ErrMsg    string  `json:"errmsg"`
	RequestID string  `json:"request_id"`
	Data      AppInfo `json:"data"`
}

type AMIdentityConfig struct {
	client          wrapper.HttpClient
	amURL           string
	amTimeoutMs     uint32
	hitConsumerSet  map[string]struct{}
	refreshNth      int
	refreshNthTimes int
	rand            *rand.Rand
	refreshNth3     int
}

func parseConfig(json gjson.Result, config *AMIdentityConfig, log log.Log) error {
	config.amURL = json.Get("am_url").String()
	if config.amURL == "" {
		return fmt.Errorf("am_url is required")
	}
	log.Debugf("[am-identity] config: am_url=%s", config.amURL)

	amService := json.Get("am_service").String()
	if amService == "" {
		return fmt.Errorf("am_service is required")
	}

	config.amTimeoutMs = uint32(json.Get("am_timeout_ms").Uint())
	if config.amTimeoutMs == 0 {
		config.amTimeoutMs = 10000
	}
	log.Debugf("[am-identity] config: am_timeout_ms=%d", config.amTimeoutMs)

	hitConsumer := json.Get("hit_consumer").String()
	if hitConsumer == "" {
		return fmt.Errorf("hit_consumer is required")
	}
	config.hitConsumerSet = initHitConsumerSet(hitConsumer)
	log.Debugf("[am-identity] config: hit_consumer=%s", hitConsumer)

	refreshNthTimes := int(json.Get("refresh_times").Int())
	if refreshNthTimes <= 0 {
		refreshNthTimes = 3
	}
	config.refreshNthTimes = refreshNthTimes

	config.refreshNth = int(json.Get("refresh_nth").Int())
	if config.refreshNth <= 0 {
		config.refreshNth = 3000
	}
	config.refreshNth3 = config.refreshNth * config.refreshNthTimes
	log.Debugf("[am-identity] config: refresh_nth=%d (probability=1/%d)", config.refreshNth, config.refreshNth)

	config.rand = rand.New(rand.NewSource(rand.Int63()))

	amDomain := json.Get("am_domain").String()
	if amDomain == "" {
		amDomain = amService
	}
	config.client = wrapper.NewClusterClient(wrapper.DnsCluster{
		ServiceName: amService,
		Port:        80,
		Domain:      amDomain,
	})
	log.Debugf("[am-identity] http client initialized for %s:80", amService)

	return nil
}

func initHitConsumerSet(hitConsumer string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, c := range strings.Split(hitConsumer, ",") {
		c = strings.TrimSpace(c)
		if c != "" {
			set[c] = struct{}{}
		}
	}
	return set
}

func onHttpRequestHeaders(ctx wrapper.HttpContext, config AMIdentityConfig, log log.Log) types.Action {
	if config.client == nil {
		log.Warnf("[am-identity] client not initialized, skipping")
		return types.ActionContinue
	}

	consumer, err := proxywasm.GetHttpRequestHeader(ConsumerKey)
	if err != nil || consumer == "" {
		return types.ActionContinue
	}

	ctx.SetContext(ConsumerKey, consumer)
	log.Debugf("[am-identity] found consumer: %s", consumer)

	if !isHitConsumer(config, consumer, log) {
		return types.ActionContinue
	}

	appID, apiKeyID, ok := getAppHeaders(log)
	if !ok {
		return types.ActionContinue
	}

	appInfo, exists := getCache(appID, log)

	needRefresh := shouldRefresh(config, exists, appID, log)

	if needRefresh && !exists {
		callSyncRefresh(ctx, config, appID, apiKeyID, log)
		return types.HeaderStopAllIterationAndWatermark
	}

	if needRefresh && exists {
		callAsyncRefresh(config, appID, apiKeyID, log)
	}

	setHeadersFromCache(appInfo, appID, apiKeyID, log)
	return types.ActionContinue
}

func isHitConsumer(config AMIdentityConfig, consumer string, log log.Log) bool {
	if _, ok := config.hitConsumerSet[consumer]; !ok {
		log.Debugf("[am-identity] consumer '%s' not in hit_consumer list, skipping", consumer)
		return false
	}
	log.Debugf("[am-identity] consumer '%s' matched, processing", consumer)
	return true
}

func getAppHeaders(log log.Log) (string, string, bool) {
	appID, err := proxywasm.GetHttpRequestHeader("x-am-appid")
	if err != nil || appID == "" {
		log.Debugf("[am-identity] x-am-appid header not found or empty, skipping")
		return "", "", false
	}
	log.Debugf("[am-identity] x-am-appid: %s", appID)

	apiKeyID, err := proxywasm.GetHttpRequestHeader("x-am-api-key-id")
	if err != nil || apiKeyID == "" {
		log.Debugf("[am-identity] x-am-api-key-id header not found or empty, skipping")
		return "", "", false
	}
	return appID, apiKeyID, true
}

func getCache(appID string, log log.Log) (*AppInfo, bool) {
	appInfo, exists := cacheMap[appID]
	if exists {
		log.Debugf("[am-identity] cache hit for key: %s", appID)
	} else {
		log.Debugf("[am-identity] cache miss for key: %s", appID)
	}
	return appInfo, exists
}

func shouldRefresh(config AMIdentityConfig, exists bool, appID string, log log.Log) bool {
	if !exists {
		randomValue := config.rand.Intn(config.refreshNth)
		if randomValue == 0 {
			log.Debugf("[am-identity] cache miss, triggered refresh by random (1/%d), appid=%s, randomValue=%d",
				config.refreshNth, appID, randomValue)
			return true
		}
		log.Debugf("[am-identity] cache miss, skip refresh this time (1/%d missed), appid=%s, randomValue=%d",
			config.refreshNth, appID, randomValue)
		return false
	}

	randomValue := config.rand.Intn(config.refreshNth3)
	if randomValue == 0 {
		log.Debugf("[am-identity] cache hit, triggered async refresh by random (1/%d), appid=%s, randomValue=%d",
			config.refreshNth3, appID, randomValue)
		return true
	}
	return false
}

func callSyncRefresh(ctx wrapper.HttpContext, config AMIdentityConfig, appID, apiKeyID string, log log.Log) {
	requestBody := fmt.Sprintf(`{"app_id":"%s"}`, appID)
	log.Debugf("[am-identity] calling API to refresh cache for appid=%s", appID)

	config.client.Post(
		config.amURL,
		[][2]string{{"Content-Type", "application/json"}},
		[]byte(requestBody),
		func(statusCode int, _ http.Header, responseBody []byte) {
			handleAPIResponse(statusCode, responseBody, appID, apiKeyID, log, true)
			proxywasm.ResumeHttpRequest()
		},
		config.amTimeoutMs,
	)
}

func callAsyncRefresh(config AMIdentityConfig, appID, apiKeyID string, log log.Log) {
	requestBody := fmt.Sprintf(`{"app_id":"%s"}`, appID)
	log.Debugf("[am-identity] async refresh cache for appid=%s", appID)

	config.client.Post(
		config.amURL,
		[][2]string{{"Content-Type", "application/json"}},
		[]byte(requestBody),
		func(statusCode int, _ http.Header, responseBody []byte) {
			handleAPIResponse(statusCode, responseBody, appID, apiKeyID, log, false)
		},
		config.amTimeoutMs,
	)
}

func handleAPIResponse(statusCode int, responseBody []byte, appID, apiKeyID string, log log.Log, sync bool) {
	if sync {
		log.Debugf("[am-identity] API response status: %d", statusCode)
	}

	if statusCode != 200 {
		if sync {
			log.Warnf("[am-identity] API call failed with status: %d, body: %s",
				statusCode, string(responseBody))
		} else {
			log.Warnf("[am-identity] async API call failed with status: %d", statusCode)
		}
		return
	}

	var apiResp APIResponse
	if err := json.Unmarshal(responseBody, &apiResp); err != nil {
		if sync {
			log.Errorf("[am-identity] failed to parse API response: %v", err)
		} else {
			log.Errorf("[am-identity] async failed to parse API response: %v", err)
		}
		return
	}

	if apiResp.Errno != 0 {
		if sync {
			log.Errorf("[am-identity] API returned error: errno=%d, errmsg=%s",
				apiResp.Errno, apiResp.ErrMsg)
		} else {
			log.Errorf("[am-identity] async API returned error: errno=%d", apiResp.Errno)
		}
		return
	}

	if sync {
		log.Debugf("[am-identity] API response parsed successfully, app_name=%s, dept_name=%s",
			apiResp.Data.AppName, apiResp.Data.DeptName)
	}

	if !validateAPIKey(apiResp.Data.APIKeys, apiKeyID) {
		if sync {
			log.Warnf("[am-identity] API key MD5 not matched for appid=%s, md5=%s", appID, apiKeyID)
		} else {
			log.Warnf("[am-identity] async API key not matched for appid=%s", appID)
		}
		return
	}

	cacheMap[appID] = &apiResp.Data
	if sync {
		log.Infof("[am-identity] cache updated for appid=%s, app_name=%s, dept_name=%s",
			appID, apiResp.Data.AppName, apiResp.Data.DeptName)
		setHeadersFromAPI(apiResp.Data, log)
	} else {
		log.Infof("[am-identity] async cache updated for appid=%s", appID)
	}
}

func validateAPIKey(keys []APIKey, apiKeyID string) bool {
	for _, key := range keys {
		if key.APIKey == apiKeyID {
			return true
		}
	}
	return false
}

func setHeadersFromAPI(app AppInfo, log log.Log) {
	consumerValue := fmt.Sprintf("API市场_%s_%s", app.DeptName, app.ZGroupName)
	if err := proxywasm.ReplaceHttpRequestHeader("x-am-consumer", consumerValue); err != nil {
		log.Warnf("[am-identity] failed to set x-am-consumer header: %v", err)
	} else {
		log.Debugf("[am-identity] set x-am-consumer: %s", consumerValue)
	}

	fullConsumerValue := fmt.Sprintf("API市场_%s_%s_%s_%s",
		app.TenantName, app.DeptName, app.ZGroupName, app.AppName)
	if err := proxywasm.AddHttpRequestHeader("x-am-full-consumer", fullConsumerValue); err != nil {
		log.Warnf("[am-identity] failed to set x-am-full-consumer header: %v", err)
	} else {
		log.Debugf("[am-identity] set x-am-full-consumer: %s", fullConsumerValue)
	}
}

func setHeadersFromCache(appInfo *AppInfo, appID, apiKeyID string, log log.Log) {
	if appInfo == nil {
		log.Debugf("[am-identity] no cached data available for appid=%s, skipping header setting", appID)
		return
	}

	if !validateAPIKey(appInfo.APIKeys, apiKeyID) {
		log.Warnf("[am-identity] cached API key invalid for appid=%s, md5=%s", appID, apiKeyID)
		return
	}

	setHeadersFromAPI(*appInfo, log)
	log.Debugf("[am-identity] successfully set consumer headers for appid=%s", appID)
}
