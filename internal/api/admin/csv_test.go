package admin

import (
	"strings"
	"testing"
)

// csvEsc 必须中和 Excel/Sheets 公式注入:以 = + - @ Tab CR 开头的字段降级为文本。
func TestCsvEsc_FormulaInjection(t *testing.T) {
	dangerous := []string{
		"=cmd|'/c calc'!A1",
		"+1+1",
		"-2+3",
		"@SUM(A1:A9)",
		"\t=evil",
	}
	for _, d := range dangerous {
		got := csvEsc(d)
		// 去掉可能的外层引号再看首字符
		unquoted := got
		if strings.HasPrefix(unquoted, `"`) && strings.HasSuffix(unquoted, `"`) {
			unquoted = unquoted[1 : len(unquoted)-1]
		}
		if unquoted == "" || unquoted[0] != '\'' {
			t.Errorf("csvEsc(%q)=%q 未中和公式前缀(应以单引号开头)", d, got)
		}
	}
}

func TestCsvEsc_QuotingAndCommas(t *testing.T) {
	// 含逗号/引号/换行需要加引号并转义内部引号
	got := csvEsc(`a,b"c
d`)
	if !strings.HasPrefix(got, `"`) || !strings.Contains(got, `""`) {
		t.Errorf("含特殊字符应当被引号包裹并转义内部引号,得到 %q", got)
	}
	// 普通文本原样返回
	if got := csvEsc("normal"); got != "normal" {
		t.Errorf("普通文本不应改动,得到 %q", got)
	}
}
