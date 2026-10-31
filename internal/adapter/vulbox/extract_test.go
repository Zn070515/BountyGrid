package vulbox

import "testing"

const srcScopeFixture = `漏洞奖励计划
SRC简介
漏洞规则
测试前须知，请查阅置顶公告内容：
联想SRC禁止上传webshell，如发现存在上传WebShell，一经查实，会冻结奖金并拉黑。
资产范围
特别注意，以下事项请认真查看并留意。
PS：联想全资子公司、联想投资、联想控股的公司的漏洞，以及与联想完全无关的漏洞，不予审核通过。
如：阳光雨露资产下漏洞暂不收录：*.sunits.com
安想、智慧医疗相关资产暂不收录，域名接管问题内部整改中暂不收录
*.lenovo.com
*.lenovo.com.cn
*.lenovomm.com
*.lenovo.cn
*.lenovo.net
*.motorola.com
*.motorola.com.cn
*.baiying.cn
*.baiying.com.cn
最新公告
其他 关于AI生成漏洞报告提交规范说明
其他 【必看】禁止上传WebShell公告`

func TestParseSRCScope(t *testing.T) {
	include, exclude, notes, found := parseSRCScope(srcScopeFixture)
	if !found {
		t.Fatal("scope section not found")
	}
	if len(include) != 9 {
		t.Fatalf("include = %v, want 9 patterns", include)
	}
	if include[0] != "*.baiying.cn" || include[len(include)-1] != "*.motorola.com.cn" {
		t.Fatalf("include not normalized/sorted: %v", include)
	}
	if len(exclude) != 1 || exclude[0] != "*.sunits.com" {
		t.Fatalf("exclude = %v, want [*.sunits.com]", exclude)
	}
	if len(notes) != 1 {
		t.Fatalf("notes = %v, want the 安想/智慧医疗 note", notes)
	}
}

func TestParseSRCScopeMissingSection(t *testing.T) {
	if _, _, _, found := parseSRCScope("no scope here"); found {
		t.Fatal("expected no scope section")
	}
}

func TestParseSRCPolicy(t *testing.T) {
	p := parseSRCPolicy(srcScopeFixture)
	if p.FileUpload != "FORBIDDEN" {
		t.Fatalf("file upload = %q, want FORBIDDEN", p.FileUpload)
	}
	if p.AIAnalysis != "CONDITIONAL" {
		t.Fatalf("ai analysis = %q, want CONDITIONAL", p.AIAnalysis)
	}
	if !p.HumanVerificationRequired {
		t.Fatal("human verification must be required")
	}
}
