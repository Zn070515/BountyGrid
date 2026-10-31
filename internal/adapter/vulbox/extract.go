package vulbox

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

// accountPage is the subset of the Vulbox user center the reader records.
// Missing fields stay at their zero value; the caller emits a fact only for
// values the page actually contained.
type accountPage struct {
	Level         string
	Exp           int
	NextLevel     string
	Reputation    int
	Season        string
	SeasonPoints  int
	SeasonRank    string
	BalanceCNY    float64
	CommissionCNY float64
	Coupons       int
	PendingReview int
	Processing    int
	Paid          int
}

var (
	reLevel        = regexp.MustCompile(`当前等级:\s*\n?\s*([^\n]+)`)
	reExp          = regexp.MustCompile(`经验值:\s*\n?\s*(\d+)`)
	reNextLevel    = regexp.MustCompile(`下一等级:\s*\n?\s*([^\n]+)`)
	reReputation   = regexp.MustCompile(`信誉(?:优秀|良好|较差)\s*\|\s*(\d+)\s*分`)
	reSeason       = regexp.MustCompile(`赛季战况\s*\n?\s*(S\d+)`)
	reSeasonPoints = regexp.MustCompile(`赛季积分\s*\n?\s*(\d+)`)
	reSeasonRank   = regexp.MustCompile(`赛季排名\s*\n?\s*([0-9+]+)`)
	reBalance      = regexp.MustCompile(`账户余额\s*\n?\s*([\d.]+)\s*元`)
	reCommission   = regexp.MustCompile(`提成金\s*\n?\s*([\d.]+)\s*元`)
	reCoupons      = regexp.MustCompile(`我的券\s*\n?\s*(\d+)\s*张`)
	rePending      = regexp.MustCompile(`待审阅\s*\n?\s*(\d+)`)
	reProcessing   = regexp.MustCompile(`处理中\s*\n?\s*(\d+)`)
	rePaid         = regexp.MustCompile(`已打款\s*\n?\s*(\d+)`)
	reJoinedCount  = regexp.MustCompile(`共\s*(\d+)\s*条数据`)
)

func parseAccountText(text string) accountPage {
	first := func(re *regexp.Regexp) string {
		m := re.FindStringSubmatch(text)
		if m == nil {
			return ""
		}
		return strings.TrimSpace(m[1])
	}
	out := accountPage{
		Level:         first(reLevel),
		NextLevel:     first(reNextLevel),
		Season:        first(reSeason),
		SeasonRank:    first(reSeasonRank),
		Exp:           firstInt(first(reExp)),
		Reputation:    firstInt(first(reReputation)),
		SeasonPoints:  firstInt(first(reSeasonPoints)),
		BalanceCNY:    firstFloat(first(reBalance)),
		CommissionCNY: firstFloat(first(reCommission)),
		Coupons:       firstInt(first(reCoupons)),
		PendingReview: firstInt(first(rePending)),
		Processing:    firstInt(first(reProcessing)),
		Paid:          firstInt(first(rePaid)),
	}
	return out
}

func firstInt(s string) int {
	v, _ := strconv.Atoi(strings.TrimSpace(s))
	return v
}

func firstFloat(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

// parseIdentityText derives identity and bank-binding state from the personal
// center text. Unknown is returned when the page does not say.
var (
	reScopePattern = regexp.MustCompile(`^\*\.[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)
	reAnyPattern   = regexp.MustCompile(`\*\.[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?`)
)

// parseSRCScope extracts the include/exclude asset patterns from an enterprise
// SRC page. found is true when the 资产范围 section exists; include may still be
// empty (the caller must fail closed rather than assume "everything").
func parseSRCScope(text string) (include, exclude, notes []string, found bool) {
	start := strings.Index(text, "资产范围")
	if start < 0 {
		return nil, nil, nil, false
	}
	section := text[start:]
	if end := strings.Index(section, "最新公告"); end >= 0 {
		section = section[:end]
	}
	found = true
	seenI := map[string]struct{}{}
	seenE := map[string]struct{}{}
	seenN := map[string]struct{}{}
	add := func(dst *[]string, seen map[string]struct{}, v string) {
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		*dst = append(*dst, v)
	}
	for _, raw := range strings.Split(section, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.Contains(line, "暂不收录") || strings.Contains(line, "暂不收录") {
			if m := reAnyPattern.FindString(strings.ToLower(line)); m != "" {
				add(&exclude, seenE, m)
			} else {
				add(&notes, seenN, line)
			}
			continue
		}
		low := strings.ToLower(line)
		if reScopePattern.MatchString(low) {
			add(&include, seenI, low)
		}
	}
	sort.Strings(include)
	sort.Strings(exclude)
	sort.Strings(notes)
	return include, exclude, notes, found
}

type srcPolicy struct {
	FileUpload                domain.CapabilityStatus
	AIAnalysis                domain.CapabilityStatus
	HumanVerificationRequired bool
}

// parseSRCPolicy only records rules the page states explicitly. Everything
// else stays UNKNOWN and is merged later with the platform FAQ.
func parseSRCPolicy(text string) srcPolicy {
	low := strings.ToLower(text)
	p := srcPolicy{}
	if strings.Contains(low, "禁止上传webshell") || strings.Contains(low, "禁止上传 web shell") {
		p.FileUpload = domain.CapForbidden
	}
	if strings.Contains(text, "AI生成漏洞报告提交规范") || strings.Contains(text, "AI辅助漏洞报告") {
		p.AIAnalysis = domain.CapConditional
		p.HumanVerificationRequired = true
	}
	return p
}

func parseIdentityText(text string) (*bool, *bool) {
	var identity *bool
	switch {
	case strings.Contains(text, "未实名"):
		v := false
		identity = &v
	case strings.Contains(text, "已实名"):
		v := true
		identity = &v
	}

	var bank *bool
	if strings.Contains(text, "银行卡") {
		switch {
		case strings.Contains(text, "银行卡未绑定"), strings.Contains(text, "未绑定银行卡"):
			v := false
			bank = &v
		case strings.Contains(text, "银行卡已绑定"), strings.Contains(text, "已绑定银行卡"):
			v := true
			bank = &v
		}
	}
	return identity, bank
}

// parseJoinedProjects extracts joined SRC names from 我的项目. The second
// return value is the page's own "共 N 条数据" count, or len(names).
func parseJoinedProjects(text string) ([]string, int) {
	seen := make(map[string]struct{})
	var names []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || len(line) > 80 {
			continue
		}
		if !strings.Contains(line, "安全应急响应中心") && !strings.Contains(line, "SRC") {
			continue
		}
		if _, dup := seen[line]; dup {
			continue
		}
		seen[line] = struct{}{}
		names = append(names, line)
	}
	count := len(names)
	if m := reJoinedCount.FindStringSubmatch(text); m != nil {
		count = firstInt(m[1])
	}
	return names, count
}
