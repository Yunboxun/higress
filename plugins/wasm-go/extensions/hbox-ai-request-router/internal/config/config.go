// config 定义插件配置结构、JSON 解析及路径匹配等公共常量。
package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
)

const (
	// DefaultMaxBodyBytes 请求 body 缓冲上限，用于 token 估算与 model 改写。
	DefaultMaxBodyBytes = 100 * 1024 * 1024

	DefaultAppIDHeader = "x-am-appid"

	MatchTypeExact  = "exact"
	MatchTypePrefix = "prefix"
	MatchTypeRegexp = "regexp"

	SourceHeader        = "header"
	SourceQuery         = "query"
	SourceAuthorization = "authorization"

	TokenModeHeuristic = "heuristic"
	TokenModeTokenize  = "tokenize"

	RouteReasonHeaderRule     = "header_rule"
	RouteReasonTokenThreshold = "token_threshold"
	RouteReasonAppIDRule      = "app_id_rule"
	RouteReasonAppModelRule   = "app_model_rule"

	// CtxKeyPendingRouteTarget 标记 header/app_id/app_model 规则已命中，待 body 阶段改写 model 后完成路由。
	CtxKeyPendingRouteTarget = "pending_route_target"
	CtxKeyPendingRouteReason = "pending_route_reason"
)

// defaultEnableOnPathSuffix 默认启用的 AI API 路径后缀，未配置 enableOnPathSuffix 时使用。
var defaultEnableOnPathSuffix = []string{
	"/completions",
	"/messages",
	"/embeddings",
	"/responses",
	"/images/generations",
	"/audio/speech",
	"/fine_tuning/jobs",
	"/moderations",
	"/image-synthesis",
	"/video-synthesis",
	"/rerank",
}

// RouteTargetConfig 路由目标 Header 与 body 改写配置。
type RouteTargetConfig struct {
	ProviderHeader   string
	ModelHeader      string
	Provider         string
	Model            string
	RewriteBodyModel bool
	ModelKey         string
}

// AppIDRule app_id 命中后转发到本地模型的匹配规则。
type AppIDRule struct {
	Header string
	Values []string
	Route  RouteTargetConfig
}

// AppModelRule app_id + 原始 model 命中后转发到目标模型的匹配规则。
type AppModelRule struct {
	Header       string
	AppIDs       []string
	SourceModels []string
	Percentage   float64
	Route        RouteTargetConfig
}

// HeuristicConfig 启发式 token 估算配置。
type HeuristicConfig struct {
	CharsPerToken float64
}

// TokenizeConfig tokenize 服务调用配置。
type TokenizeConfig struct {
	Client      wrapper.HttpClient
	ServiceName string
	ServicePort int64
	Path        string
	Timeout     uint32
	ModelField  string
}

// TokenThresholdConfig token 阈值路由配置。
type TokenThresholdConfig struct {
	Enabled   bool
	Threshold int64
	Mode      string
	Heuristic HeuristicConfig
	Tokenize  TokenizeConfig
}

// HeaderRule header / query / authorization 匹配规则。
type HeaderRule struct {
	Source    string
	Key       string
	MatchType string
	Values    []string
	Pattern   *regexp.Regexp
}

// PluginConfig 插件完整配置。
type PluginConfig struct {
	Enabled            bool
	EnableOnPathSuffix []string
	ExternalRouting    RouteTargetConfig
	LocalRouting       *RouteTargetConfig
	TokenThreshold     TokenThresholdConfig
	HeaderRules        []HeaderRule
	AppIDRules         []AppIDRule
	AppModelRules      []AppModelRule
}

// ParseConfig 解析插件 JSON 配置。
func ParseConfig(json gjson.Result, config *PluginConfig) error {
	config.Enabled = true
	if json.Get("enabled").Exists() {
		config.Enabled = json.Get("enabled").Bool()
	}

	config.EnableOnPathSuffix = parsePathSuffixes(json.Get("enableOnPathSuffix"))

	if err := parseRouteTarget(json.Get("externalRouting"), &config.ExternalRouting, false, "externalRouting"); err != nil {
		return err
	}
	if err := parseLocalRouting(json.Get("localRouting"), &config.LocalRouting); err != nil {
		return err
	}
	if err := parseTokenThreshold(json.Get("tokenThreshold"), &config.TokenThreshold); err != nil {
		return err
	}
	if err := parseHeaderRules(json.Get("headerRules"), &config.HeaderRules); err != nil {
		return err
	}
	if err := parseAppIDRules(json.Get("appIdRules"), &config.AppIDRules); err != nil {
		return err
	}
	if err := parseAppModelRules(json.Get("appModelRules"), &config.AppModelRules); err != nil {
		return err
	}

	localProvider := ""
	localModel := ""
	if config.LocalRouting != nil {
		localProvider = config.LocalRouting.Provider
		localModel = config.LocalRouting.Model
	}
	log.Debugf("config parsed: enabled=%v pathSuffixes=%v headerRules=%d appIdRules=%d appModelRules=%d externalProvider=%s externalModel=%s localProvider=%s localModel=%s rewriteBodyModel=%v tokenThresholdEnabled=%v",
		config.Enabled, config.EnableOnPathSuffix, len(config.HeaderRules), len(config.AppIDRules), len(config.AppModelRules),
		config.ExternalRouting.Provider, config.ExternalRouting.Model, localProvider, localModel,
		config.ExternalRouting.RewriteBodyModel, config.TokenThreshold.Enabled)
	for i, rule := range config.HeaderRules {
		log.Debugf("config headerRules[%d]: source=%s key=%s matchType=%s values=%v",
			i, rule.Source, rule.Key, rule.MatchType, rule.Values)
	}
	for i, rule := range config.AppIDRules {
		log.Debugf("config appIdRules[%d]: header=%s values=%v provider=%s model=%s",
			i, rule.Header, rule.Values, rule.Route.Provider, rule.Route.Model)
	}
	for i, rule := range config.AppModelRules {
		log.Debugf("config appModelRules[%d]: header=%s appIds=%v sourceModels=%v provider=%s model=%s",
			i, rule.Header, rule.AppIDs, rule.SourceModels, rule.Route.Provider, rule.Route.Model)
	}

	return nil
}

// ParseHeaderRule 解析单条 header 匹配规则。
func ParseHeaderRule(json gjson.Result) (HeaderRule, error) {
	return parseHeaderRule(json)
}

func parsePathSuffixes(json gjson.Result) []string {
	if !json.Exists() || !json.IsArray() {
		return append([]string(nil), defaultEnableOnPathSuffix...)
	}
	suffixes := make([]string, 0, len(json.Array()))
	for _, item := range json.Array() {
		suffixes = append(suffixes, item.String())
	}
	return suffixes
}

func parseLocalRouting(json gjson.Result, cfg **RouteTargetConfig) error {
	if !json.Exists() {
		return nil
	}
	route := &RouteTargetConfig{}
	if err := parseRouteTarget(json, route, true, "localRouting"); err != nil {
		return err
	}
	*cfg = route
	return nil
}

func parseRouteTarget(json gjson.Result, cfg *RouteTargetConfig, required bool, fieldName string) error {
	cfg.ProviderHeader = json.Get("providerHeader").String()
	if cfg.ProviderHeader == "" {
		cfg.ProviderHeader = "x-higress-llm-provider"
	}
	cfg.ModelHeader = json.Get("modelHeader").String()
	if cfg.ModelHeader == "" {
		cfg.ModelHeader = "x-higress-llm-model"
	}
	cfg.Provider = json.Get("provider").String()
	cfg.Model = json.Get("model").String()
	if required && (cfg.Provider == "" || cfg.Model == "") {
		return fmt.Errorf("%s.provider and %s.model are required", fieldName, fieldName)
	}
	cfg.RewriteBodyModel = true
	if json.Get("rewriteBodyModel").Exists() {
		cfg.RewriteBodyModel = json.Get("rewriteBodyModel").Bool()
	}
	cfg.ModelKey = json.Get("modelKey").String()
	if cfg.ModelKey == "" {
		cfg.ModelKey = "model"
	}
	return nil
}

func parseTokenThreshold(json gjson.Result, cfg *TokenThresholdConfig) error {
	if !json.Exists() {
		return nil
	}
	cfg.Enabled = json.Get("enabled").Bool()
	cfg.Threshold = json.Get("threshold").Int()
	if !cfg.Enabled {
		return nil
	}
	if cfg.Threshold <= 0 {
		return errors.New("tokenThreshold.threshold must be greater than 0 when enabled")
	}

	cfg.Mode = json.Get("mode").String()
	if cfg.Mode == "" {
		cfg.Mode = TokenModeHeuristic
	}
	if cfg.Mode != TokenModeHeuristic && cfg.Mode != TokenModeTokenize {
		return fmt.Errorf("tokenThreshold.mode must be %q or %q", TokenModeHeuristic, TokenModeTokenize)
	}

	heuristic := json.Get("heuristic")
	cfg.Heuristic.CharsPerToken = heuristic.Get("charsPerToken").Float()
	if cfg.Heuristic.CharsPerToken <= 0 {
		cfg.Heuristic.CharsPerToken = 4
	}

	if cfg.Mode != TokenModeTokenize {
		return nil
	}

	tokenize := json.Get("tokenize")
	serviceName := tokenize.Get("serviceName").String()
	if serviceName == "" {
		return errors.New("tokenThreshold.tokenize.serviceName is required when mode is tokenize")
	}
	servicePort := tokenize.Get("servicePort").Int()
	if servicePort <= 0 {
		servicePort = 8000
	}
	path := tokenize.Get("path").String()
	if path == "" {
		path = "/v1/tokenize"
	}
	timeout := tokenize.Get("timeout").Int()
	if timeout <= 0 {
		timeout = 3000
	}
	modelField := tokenize.Get("modelField").String()
	if modelField == "" {
		modelField = "model"
	}

	cfg.Tokenize = TokenizeConfig{
		ServiceName: serviceName,
		ServicePort: servicePort,
		Path:        path,
		Timeout:     uint32(timeout),
		ModelField:  modelField,
		Client: wrapper.NewClusterClient(wrapper.FQDNCluster{
			FQDN: serviceName,
			Port: servicePort,
		}),
	}
	return nil
}

func parseHeaderRules(json gjson.Result, rules *[]HeaderRule) error {
	if !json.Exists() || !json.IsArray() {
		return nil
	}
	for i, item := range json.Array() {
		rule, err := parseHeaderRule(item)
		if err != nil {
			return fmt.Errorf("headerRules[%d]: %w", i, err)
		}
		*rules = append(*rules, rule)
	}
	return nil
}

func parseAppIDRules(json gjson.Result, rules *[]AppIDRule) error {
	if !json.Exists() || !json.IsArray() {
		return nil
	}
	for i, item := range json.Array() {
		rule, err := parseAppIDRule(item)
		if err != nil {
			return fmt.Errorf("appIdRules[%d]: %w", i, err)
		}
		*rules = append(*rules, rule)
	}
	return nil
}

func parseAppIDRule(json gjson.Result) (AppIDRule, error) {
	rule := AppIDRule{
		Header: strings.ToLower(json.Get("header").String()),
	}
	if rule.Header == "" {
		rule.Header = DefaultAppIDHeader
	}

	values := json.Get("values")
	if !values.Exists() || !values.IsArray() || len(values.Array()) == 0 {
		return AppIDRule{}, errors.New("values is required")
	}
	for _, value := range values.Array() {
		rule.Values = append(rule.Values, value.String())
	}

	if err := parseRouteTarget(json.Get("route"), &rule.Route, true, "appIdRules[].route"); err != nil {
		return AppIDRule{}, err
	}
	return rule, nil
}

func parseAppModelRules(json gjson.Result, rules *[]AppModelRule) error {
	if !json.Exists() || !json.IsArray() {
		return nil
	}
	for i, item := range json.Array() {
		rule, err := parseAppModelRule(item)
		if err != nil {
			return fmt.Errorf("appModelRules[%d]: %w", i, err)
		}
		*rules = append(*rules, rule)
	}
	return nil
}

func parseAppModelRule(json gjson.Result) (AppModelRule, error) {
	rule := AppModelRule{
		Header: strings.ToLower(json.Get("header").String()),
	}
	if rule.Header == "" {
		rule.Header = DefaultAppIDHeader
	}

	appIDs := json.Get("appIds")
	if !appIDs.Exists() || !appIDs.IsArray() || len(appIDs.Array()) == 0 {
		return AppModelRule{}, errors.New("appIds is required")
	}
	for _, value := range appIDs.Array() {
		rule.AppIDs = append(rule.AppIDs, value.String())
	}

	sourceModels := json.Get("sourceModels")
	if !sourceModels.Exists() || !sourceModels.IsArray() || len(sourceModels.Array()) == 0 {
		return AppModelRule{}, errors.New("sourceModels is required")
	}
	for _, value := range sourceModels.Array() {
		rule.SourceModels = append(rule.SourceModels, value.String())
	}

	percentage := json.Get("percentage")
	if !percentage.Exists() {
		rule.Percentage = 100.0
	} else {
		rule.Percentage = percentage.Float()
		if rule.Percentage < 0 || rule.Percentage > 100 {
			return AppModelRule{}, errors.New("percentage must be between 0 and 100")
		}
	}

	if err := parseRouteTarget(json.Get("route"), &rule.Route, true, "appModelRules[].route"); err != nil {
		return AppModelRule{}, err
	}
	return rule, nil
}

func parseHeaderRule(json gjson.Result) (HeaderRule, error) {
	rule := HeaderRule{
		Source: strings.ToLower(json.Get("source").String()),
		Key:    json.Get("key").String(),
	}
	if rule.Source == "" {
		rule.Source = SourceHeader
	}
	switch rule.Source {
	case SourceHeader, SourceQuery, SourceAuthorization:
	default:
		return HeaderRule{}, fmt.Errorf("unsupported source %q", rule.Source)
	}
	if rule.Source != SourceAuthorization && rule.Key == "" {
		return HeaderRule{}, errors.New("key is required when source is header or query")
	}

	rule.MatchType = strings.ToLower(json.Get("matchType").String())
	if rule.MatchType == "" {
		rule.MatchType = MatchTypeExact
	}
	switch rule.MatchType {
	case MatchTypeExact, MatchTypePrefix, MatchTypeRegexp:
	default:
		return HeaderRule{}, fmt.Errorf("unsupported matchType %q", rule.MatchType)
	}

	if rule.MatchType == MatchTypeRegexp {
		pattern := json.Get("pattern").String()
		if pattern == "" {
			return HeaderRule{}, errors.New("pattern is required when matchType is regexp")
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return HeaderRule{}, fmt.Errorf("invalid regexp pattern: %w", err)
		}
		rule.Pattern = compiled
		return rule, nil
	}

	values := json.Get("values")
	if !values.Exists() || !values.IsArray() || len(values.Array()) == 0 {
		return HeaderRule{}, errors.New("values is required when matchType is exact or prefix")
	}
	for _, value := range values.Array() {
		rule.Values = append(rule.Values, value.String())
	}
	return rule, nil
}

// PathMatchesSuffix 检查请求路径是否匹配任一后缀。
func PathMatchesSuffix(path string, suffixes []string) bool {
	if idx := strings.Index(path, "?"); idx != -1 {
		path = path[:idx]
	}
	for _, suffix := range suffixes {
		if suffix == "*" || strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}
