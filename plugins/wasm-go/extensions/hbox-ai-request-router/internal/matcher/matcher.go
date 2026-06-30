// matcher 根据 header、query、Authorization、app_id 或 app_id+model 中的值匹配路由规则。
package matcher

import (
	"math/rand"
	"net/url"
	"strings"
	"time"

	"hbox-ai-request-router/internal/config"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/wrapper"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

// MatchHeaderRules 检查是否命中任一 header 规则。
func MatchHeaderRules(ctx wrapper.HttpContext, rules []config.HeaderRule) (bool, string) {
	log.Debugf("evaluating header rules: count=%d", len(rules))
	for i, rule := range rules {
		value, ok := extractRuleValue(ctx, rule)
		if !ok || value == "" {
			log.Debugf("header rule[%d] skipped: source=%s key=%s matchType=%s reason=value_missing_or_empty",
				i, rule.Source, rule.Key, rule.MatchType)
			continue
		}
		log.Debugf("header rule[%d] extracted: source=%s key=%s matchType=%s value=%q expectedValues=%v",
			i, rule.Source, rule.Key, rule.MatchType, value, rule.Values)
		if matchValue(rule, value) {
			log.Debugf("header rule[%d] matched: source=%s key=%s value=%q", i, rule.Source, rule.Key, value)
			return true, config.RouteReasonHeaderRule
		}
		log.Debugf("header rule[%d] not matched: source=%s key=%s value=%q expectedValues=%v matchType=%s",
			i, rule.Source, rule.Key, value, rule.Values, rule.MatchType)
	}
	log.Debugf("no header rules matched after evaluating %d rules", len(rules))
	return false, ""
}

// MatchAppIDRules 检查是否命中任一 app_id 规则。
func MatchAppIDRules(ctx wrapper.HttpContext, rules []config.AppIDRule) (bool, *config.RouteTargetConfig, string) {
	log.Debugf("evaluating app_id rules: count=%d", len(rules))
	for i, rule := range rules {
		headerName := rule.Header
		if headerName == "" {
			headerName = config.DefaultAppIDHeader
		}
		value, err := proxywasm.GetHttpRequestHeader(headerName)
		if err != nil || value == "" {
			log.Debugf("app_id rule[%d] skipped: header=%s reason=value_missing_or_empty err=%v", i, headerName, err)
			continue
		}
		log.Debugf("app_id rule[%d] extracted: header=%s value=%q expectedValues=%v", i, headerName, value, rule.Values)
		for _, expected := range rule.Values {
			if value == expected {
				log.Debugf("app_id rule[%d] matched: header=%s value=%q provider=%s model=%s",
					i, headerName, value, rule.Route.Provider, rule.Route.Model)
				route := rule.Route
				return true, &route, config.RouteReasonAppIDRule
			}
		}
		log.Debugf("app_id rule[%d] not matched: header=%s value=%q expectedValues=%v", i, headerName, value, rule.Values)
	}
	log.Debugf("no app_id rules matched after evaluating %d rules", len(rules))
	return false, nil, ""
}

// MatchAppModelRules 检查是否命中任一 app_id + source model 规则。
func MatchAppModelRules(appID string, sourceModel string, rules []config.AppModelRule) (bool, *config.RouteTargetConfig, string) {
	log.Debugf("evaluating app_model rules: count=%d appID=%q sourceModel=%q", len(rules), appID, sourceModel)
	if appID == "" || sourceModel == "" {
		return false, nil, ""
	}
	for i, rule := range rules {
		if !containsExact(rule.AppIDs, appID) {
			log.Debugf("app_model rule[%d] skipped: appID=%q not in %v", i, appID, rule.AppIDs)
			continue
		}
		if !containsExact(rule.SourceModels, sourceModel) {
			log.Debugf("app_model rule[%d] skipped: sourceModel=%q not in %v", i, sourceModel, rule.SourceModels)
			continue
		}

		if rule.Percentage < 100.0 {
			if float64(rand.Intn(10000))/100.0 >= rule.Percentage {
				log.Debugf("app_model rule[%d] matched but skipped due to percentage: %.2f%%", i, rule.Percentage)
				continue
			}
		}

		log.Debugf("app_model rule[%d] matched: appID=%q sourceModel=%q provider=%s model=%s percentage=%.2f%%",
			i, appID, sourceModel, rule.Route.Provider, rule.Route.Model, rule.Percentage)
		route := rule.Route
		return true, &route, config.RouteReasonAppModelRule
	}
	log.Debugf("no app_model rules matched after evaluating %d rules", len(rules))
	return false, nil, ""
}

// GetAppID 从请求头中提取 app_id。
func GetAppID(ctx wrapper.HttpContext, headerName string) string {
	if headerName == "" {
		headerName = config.DefaultAppIDHeader
	}
	value, err := proxywasm.GetHttpRequestHeader(headerName)
	if err != nil {
		log.Debugf("failed to get app_id header %s: %v", headerName, err)
		return ""
	}
	return value
}

func containsExact(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// extractRuleValue 按规则来源从请求中提取待匹配的值。
func extractRuleValue(ctx wrapper.HttpContext, rule config.HeaderRule) (string, bool) {
	switch rule.Source {
	case config.SourceHeader:
		value, err := proxywasm.GetHttpRequestHeader(rule.Key)
		if err != nil {
			log.Debugf("failed to get header %s: %v", rule.Key, err)
			return "", false
		}
		return value, true
	case config.SourceQuery:
		values, err := parsePathQuery(ctx.Path())
		if err != nil {
			log.Debugf("failed to parse path query: %v", err)
			return "", false
		}
		matched, ok := values[rule.Key]
		if !ok || len(matched) == 0 {
			return "", false
		}
		return matched[0], true
	case config.SourceAuthorization:
		auth, err := proxywasm.GetHttpRequestHeader("Authorization")
		if err != nil || auth == "" {
			return "", false
		}
		return ExtractAuthorizationToken(auth)
	default:
		return "", false
	}
}

// parsePathQuery 从 :path 中解析 query 参数。
func parsePathQuery(path string) (url.Values, error) {
	if idx := strings.Index(path, "?"); idx != -1 {
		return url.ParseQuery(path[idx+1:])
	}
	parsed, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	return parsed.Query(), nil
}

// ExtractAuthorizationToken 从 Authorization 头提取 Bearer token；非 Bearer scheme 返回 false。
func ExtractAuthorizationToken(auth string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return "", false
	}
	token := ExtractBearerToken(auth)
	if token == "" {
		return "", false
	}
	return token, true
}

// ExtractBearerToken 从 Authorization 头提取 Bearer token。
func ExtractBearerToken(auth string) string {
	const prefix = "Bearer "
	if strings.HasPrefix(auth, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(auth, prefix))
	}
	return strings.TrimSpace(auth)
}

// MatchValue 检查值是否满足匹配规则。
func MatchValue(rule config.HeaderRule, value string) bool {
	return matchValue(rule, value)
}

// matchValue 按 exact / prefix / regexp 三种方式判断值是否命中。
func matchValue(rule config.HeaderRule, value string) bool {
	switch rule.MatchType {
	case config.MatchTypeExact:
		for _, expected := range rule.Values {
			if value == expected {
				return true
			}
		}
	case config.MatchTypePrefix:
		for _, expected := range rule.Values {
			if strings.HasPrefix(value, expected) {
				return true
			}
		}
	case config.MatchTypeRegexp:
		if rule.Pattern != nil {
			return rule.Pattern.MatchString(value)
		}
	}
	return false
}
