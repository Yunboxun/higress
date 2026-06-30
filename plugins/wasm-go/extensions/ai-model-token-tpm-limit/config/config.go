package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
)

const (
	Second           int64 = 1
	SecondsPerMinute       = 60 * Second
	SecondsPerHour         = 60 * SecondsPerMinute
	SecondsPerDay          = 24 * SecondsPerHour

	DefaultRejectedCode uint32 = 429
	DefaultRejectedMsg  string = "Too many requests"
)

var timeWindows = map[string]int64{
	"token_per_second": Second,
	"token_per_minute": SecondsPerMinute,
	"token_per_hour":   SecondsPerHour,
	"token_per_day":    SecondsPerDay,
}

type PluginConfig struct {
	RuleName        string
	LimitByHeader   string
	GlobalThreshold *GlobalThreshold
	RuleItems       []RuleItem
	RejectedCode    uint32
	RejectedMsg     string
	RedisClient     wrapper.RedisClient
}

type GlobalThreshold struct {
	Count      int64
	TimeWindow int64
	LimitKeys  []LimitKey
}

func (g *GlobalThreshold) MatchKey(key string) bool {
	if len(g.LimitKeys) == 0 {
		return true
	}
	for _, lk := range g.LimitKeys {
		if lk.Key == key {
			return true
		}
	}
	return false
}

func (g *GlobalThreshold) GetLimitForKey(key string) (count int64, window int64) {
	for _, lk := range g.LimitKeys {
		if lk.Key == key {
			return lk.Count, lk.TimeWindow
		}
	}
	return g.Count, g.TimeWindow
}

type RuleItem struct {
	LimitByHeader string
	MatchModels   []ModelItem
}

type ModelItem struct {
	Key       string
	LimitKeys []LimitKey
}

type LimitKey struct {
	Key        string
	Count      int64
	TimeWindow int64
}

func ParsePluginConfig(json gjson.Result, cfg *PluginConfig) error {
	err := initRedisClient(json, cfg)
	if err != nil {
		log.Errorf("[ai-model-token-tpm-limit] redis init error: %v", err)
		return err
	}

	ruleName := json.Get("rule_name")
	if !ruleName.Exists() || ruleName.String() == "" {
		cfg.RuleName = "default_ai_model_token_tpm_limit"
	} else {
		cfg.RuleName = ruleName.String()
	}

	if rejectedCode := json.Get("rejected_code"); rejectedCode.Exists() {
		cfg.RejectedCode = uint32(rejectedCode.Uint())
	} else {
		cfg.RejectedCode = DefaultRejectedCode
	}
	if rejectedMsg := json.Get("rejected_msg"); rejectedMsg.Exists() {
		cfg.RejectedMsg = rejectedMsg.String()
	} else {
		cfg.RejectedMsg = DefaultRejectedMsg
	}

	globalResult := json.Get("global_threshold")
	if globalResult.Exists() {
		threshold, limitByHeader, err := parseGlobalThreshold(globalResult)
		if err == nil {
			cfg.GlobalThreshold = threshold
			if limitByHeader != "" {
				cfg.LimitByHeader = limitByHeader
			}
		}
	}

	ruleItemsResult := json.Get("rule_items")
	if ruleItemsResult.Exists() {
		items, headerName, err := parseRuleItems(ruleItemsResult)
		if err != nil {
			log.Warnf("[ai-model-token-tpm-limit] model rule parsing failed: %v", err)
		} else {
			cfg.RuleItems = items
			if cfg.LimitByHeader == "" && headerName != "" {
				cfg.LimitByHeader = headerName
			}
		}
	}

	if cfg.LimitByHeader == "" {
		if lbh := json.Get("global_threshold.limit_by_header"); lbh.Exists() && lbh.String() != "" {
			cfg.LimitByHeader = lbh.String()
		} else if lbh := json.Get("rule_items.0.limit_by_header"); lbh.Exists() && lbh.String() != "" {
			cfg.LimitByHeader = lbh.String()
		} else {
			cfg.LimitByHeader = "x-am-appid"
		}
	}

	return nil
}

func initRedisClient(json gjson.Result, cfg *PluginConfig) error {
	redisConfig := json.Get("redis")
	if !redisConfig.Exists() {
		return errors.New("missing redis in config")
	}

	serviceName := redisConfig.Get("service_name").String()
	if serviceName == "" {
		return errors.New("redis service name must not be empty")
	}

	servicePort := int(redisConfig.Get("service_port").Int())
	if servicePort == 0 {
		if strings.HasSuffix(serviceName, ".static") {
			servicePort = 80
		} else {
			servicePort = 6379
		}
	}

	username := redisConfig.Get("username").String()
	password := redisConfig.Get("password").String()
	timeout := int(redisConfig.Get("timeout").Int())
	if timeout == 0 {
		timeout = 1000
	}

	cfg.RedisClient = wrapper.NewRedisClusterClient(wrapper.FQDNCluster{
		FQDN: serviceName,
		Port: int64(servicePort),
	})
	database := int(redisConfig.Get("database").Int())
	err := cfg.RedisClient.Init(username, password, int64(timeout), wrapper.WithDataBase(database))
	if err != nil {
		log.Errorf("[ai-model-token-tpm-limit] redis init returned error: %v", err)
	}
	if cfg.RedisClient.Ready() {
		log.Info("[ai-model-token-tpm-limit] redis init successfully")
	}
	return nil
}

func parseGlobalThreshold(item gjson.Result) (*GlobalThreshold, string, error) {
	threshold := &GlobalThreshold{}

	for timeWindowKey, duration := range timeWindows {
		q := item.Get(timeWindowKey)
		if q.Exists() {
			count := q.Int()
			if count <= 0 {
				return nil, "", fmt.Errorf("'%s' must be a positive integer, got %d", timeWindowKey, count)
			}
			threshold.Count = count
			threshold.TimeWindow = duration
			break
		}
	}

	limitByHeader := ""
	if lbh := item.Get("limit_by_header"); lbh.Exists() && lbh.String() != "" {
		limitByHeader = lbh.String()
	}

	limitKeysResult := item.Get("limit_keys")
	if limitKeysResult.Exists() {
		for _, lkItem := range limitKeysResult.Array() {
			key := lkItem.Get("key").String()
			if key == "" {
				continue
			}

			lk := LimitKey{Key: key}
			for twKey, dur := range timeWindows {
				if q := lkItem.Get(twKey); q.Exists() {
					lk.Count = q.Int()
					lk.TimeWindow = dur
					break
				}
			}
			if lk.Count == 0 {
				if threshold.Count > 0 {
					lk.Count = threshold.Count
					lk.TimeWindow = threshold.TimeWindow
				} else {
					continue
				}
			}
			threshold.LimitKeys = append(threshold.LimitKeys, lk)
		}
	}

	if threshold.Count == 0 && len(threshold.LimitKeys) > 0 {
		threshold.Count = threshold.LimitKeys[0].Count
		threshold.TimeWindow = threshold.LimitKeys[0].TimeWindow
	}

	if threshold.Count == 0 && len(threshold.LimitKeys) == 0 {
		return nil, "", errors.New("global_threshold must have at least a time window or limit_keys with time windows")
	}

	return threshold, limitByHeader, nil
}

func parseRuleItems(ruleItemsResult gjson.Result) ([]RuleItem, string, error) {
	var ruleItems []RuleItem
	var headerName string

	for _, item := range ruleItemsResult.Array() {
		var ruleItem RuleItem

		limitByHeader := item.Get("limit_by_header")
		if limitByHeader.Exists() && limitByHeader.String() != "" {
			ruleItem.LimitByHeader = limitByHeader.String()
			if headerName == "" {
				headerName = limitByHeader.String()
			}
		}

		matchModelsResult := item.Get("match_models")
		if !matchModelsResult.Exists() {
			continue
		}

		for _, modelResult := range matchModelsResult.Array() {
			modelKey := modelResult.Get("key").String()
			if modelKey == "" {
				continue
			}

			modelItem := ModelItem{Key: strings.TrimSpace(modelKey)}
			limitKeysResult := modelResult.Get("limit_keys")
			if !limitKeysResult.Exists() {
				continue
			}

			for _, lkResult := range limitKeysResult.Array() {
				key := lkResult.Get("key").String()
				if key == "" {
					continue
				}

				lk := LimitKey{Key: key}
				found := false
				for twKey, dur := range timeWindows {
					if q := lkResult.Get(twKey); q.Exists() {
						lk.Count = q.Int()
						lk.TimeWindow = dur
						found = true
						break
					}
				}
				if !found || lk.Count <= 0 {
					continue
				}

				modelItem.LimitKeys = append(modelItem.LimitKeys, lk)
			}

			if len(modelItem.LimitKeys) > 0 {
				ruleItem.MatchModels = append(ruleItem.MatchModels, modelItem)
			}
		}

		if len(ruleItem.MatchModels) > 0 {
			ruleItems = append(ruleItems, ruleItem)
		}
	}

	return ruleItems, headerName, nil
}
