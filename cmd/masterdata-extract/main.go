// Command masterdata-extract 把 Wiki 标定数值提取成 internal/masterdata 吃的格式。
//
// 输入(都不入版本库,铁律三):
//
//	-templates  magireco-wiki-data 的 data/archive/templates.jsonl.gz
//	-characters magireco-wiki-data 的 data/characters.json(取 charaId)
//	-memoria    magireco-wiki-data 的 data/memoria.json
//	-effects    效果名映射表(中文效果名 → code/sub),见下
//
// 输出:一份 masterdata.File JSON。
//
// # 为什么映射表是外部文件而不是写死在代码里
//
// Wiki 的效果是**中文散文**——"（全体）属性强化伤害，效果值：305%，成长：10%"。
// 拆出结构(目标、数值、成长、发动率、回合)是机械活,本程序全做;但把"属性强化伤害"
// 对应到抓包的哪个 code/sub,是**领域判断**。全量 214 个效果名里,长尾那部分需要
// 懂这个游戏的人来定。
//
// 把它做成数据文件的好处是:它可以被评审、被 diff、被逐条讨论,而不是藏在一个
// switch 里由写代码的人独自拍板。
//
// # 遇到没映射的效果名一律失败
//
// 不静默跳过。跳过的后果是**一份看起来正常、实则缺了效果的 master data**——
// 服务端照常启动,战斗照常打,只是某些技能悄悄没有了。那种缺陷要靠玩家察觉
// "这个技能好像没生效"才会暴露。
//
// 失败时把未映射的效果名按出现频次排序打出来,便于优先补高频的那些。
package main

import (
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"magirecocn-revival/api-server/internal/masterdata"
)

func main() {
	var (
		tmplPath  = flag.String("templates", "", "templates.jsonl.gz 路径")
		charaPath = flag.String("characters", "", "characters.json 路径")
		memoPath  = flag.String("memoria", "", "memoria.json 路径")
		effPath   = flag.String("effects", "", "效果名映射表 JSON 路径")
		outPath   = flag.String("out", "", "输出路径,留空写 stdout")
		version   = flag.String("version", "", "写进产物的版本标识(建议用构建日期)")
		reportGap = flag.Bool("report-gaps", false, "只报告未映射的效果名,不产出数据")
		gapsCSV   = flag.String("gaps-csv", "", "把缺口清单写成 CSV(供交给领域研究人员填写)")
	)
	flag.Parse()

	if *tmplPath == "" || *effPath == "" {
		fmt.Fprintln(os.Stderr, "必须提供 -templates 与 -effects;用 -h 看全部参数")
		os.Exit(2)
	}
	if err := run(*tmplPath, *charaPath, *memoPath, *effPath, *outPath, *version, *reportGap, *gapsCSV); err != nil {
		fmt.Fprintln(os.Stderr, "提取失败:", err)
		os.Exit(1)
	}
}

// effectMap 是映射表的线格式:中文效果名 → 结构化的 code/sub。
//
//	{ "属性强化伤害": {"code": "ATTACK"},
//	  "攻击力UP":     {"code": "BUFF", "sub": "ATTACK"} }
type effectMap map[string]struct {
	Code string `json:"code"`
	Sub  string `json:"sub,omitempty"`
}

// targets 是 Wiki 里的目标写法 → 抓包封闭集合的映射。
//
// 这一张**可以**写死在代码里:14 → 7 的对应是可判定的(把"随机4次"归到 RANDOM5
// 这类合并有据可依),而且目标集合本身已经被抓包钉成封闭的。效果名不同——那是
// 214 条开放式的领域知识。
var targets = map[string]string{
	"全体": "ALL", "敌方全体": "ALL", "我方全体": "ALL",
	"自身": "SELF",
	"目标": "TARGET", "敌方单体": "TARGET",
	"单体": "ONE", "我方单体": "ONE",
	"连携": "CONNECT",
	// 随机 N 次与方向性选取在上游都归到这两个:RANDOM5 是"随机若干",
	// 纵/横方向是受限选取。合并会丢一点粒度,但上游本来就只有 7 个取值。
	"随机5": "RANDOM5", "随机4": "RANDOM5", "随机5次": "RANDOM5", "随机4次": "RANDOM5",
	"随机3": "RANDOM5", "随机3次": "RANDOM5",
	"纵方向": "LIMITED", "横方向": "LIMITED",
}

var (
	// 模板参数:| 键 = 值
	paramRe = regexp.MustCompile(`(?m)^\s*\|\s*([^=\n|]+?)\s*=\s*(.*)$`)
	// 效果片段:（目标）效果名，效果值：305%，成长：10%，发动率：40%，持续回合：1
	segRe    = regexp.MustCompile(`（([^）]{1,6})）([^，,]+)`)
	numRe    = regexp.MustCompile(`(\d+)`)
	rarityRe = regexp.MustCompile(`^(HP|ATK|DEF)(最小|最大)(\d)$`)
)

var attributes = map[string]string{
	"火": "FIRE", "水": "WATER", "木": "TIMBER",
	"光": "LIGHT", "暗": "DARK", "无": "VOID",
}

func run(tmplPath, charaPath, memoPath, effPath, outPath, version string, reportGap bool, gapsCSV string) error {
	rawEffects := effectMap{}
	if err := readJSON(effPath, &rawEffects); err != nil {
		return fmt.Errorf("读取效果映射表: %w", err)
	}
	// 表里的键也归一化,让写表的人不必操心空格。
	effects := make(effectMap, len(rawEffects))
	for k, v := range rawEffects {
		effects[normalizeName(k)] = v
	}

	// charaId 从 characters.json 取:模板页只有角色名,而服务端要的是上游 ID。
	nameToID := map[string]int{}
	if charaPath != "" {
		// charaId 在 characters.json 里有时是字符串有时是数字(Wiki 转录的产物),
		// 用 json.Number 两种都吃。
		var raw map[string]struct {
			CharaID json.Number `json:"charaId"`
			NameJa  string      `json:"nameJa"`
			NameZh  string      `json:"nameZh"`
		}
		if err := readJSON(charaPath, &raw); err != nil {
			return fmt.Errorf("读取 characters.json: %w", err)
		}
		for _, c := range raw {
			id, err := c.CharaID.Int64()
			if err != nil || id == 0 {
				continue
			}
			// 模板页名用的是日文名(Template:角色数据表/七海 やちよ)。
			for _, n := range []string{c.NameJa, c.NameZh} {
				if n != "" {
					nameToID[normalizeName(n)] = int(id)
				}
			}
		}
	}

	gaps := gapSet{}
	charas, err := extractCharas(tmplPath, nameToID, effects, gaps)
	if err != nil {
		return err
	}

	var memoria []masterdata.Memoria
	if memoPath != "" {
		if memoria, err = extractMemoria(memoPath, effects, gaps); err != nil {
			return err
		}
	}

	if len(gaps) > 0 {
		reportGaps(gaps)
		if gapsCSV != "" {
			if err := writeGapsCSV(gaps, gapsCSV); err != nil {
				return fmt.Errorf("写出缺口 CSV: %w", err)
			}
			fmt.Fprintf(os.Stderr, "缺口清单已写出: %s\n", gapsCSV)
		}
		if !reportGap {
			return fmt.Errorf("有 %d 个效果名未映射;补全 -effects 表后重跑,或用 -report-gaps 只看清单", len(gaps))
		}
		return nil
	}
	if reportGap {
		fmt.Fprintln(os.Stderr, "没有未映射的效果名。")
		return nil
	}

	out := masterdata.File{Version: version, Charas: charas, Memoria: memoria}
	blob, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if outPath == "" {
		_, err = os.Stdout.Write(blob)
		return err
	}
	return os.WriteFile(outPath, blob, 0o644)
}

func extractCharas(path string, nameToID map[string]int, effects effectMap, gaps gapSet) ([]masterdata.Chara, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	var out []masterdata.Chara
	dec := json.NewDecoder(zr)
	for dec.More() {
		var page struct {
			Title    string `json:"title"`
			Wikitext string `json:"wikitext"`
		}
		if err := dec.Decode(&page); err != nil {
			return nil, err
		}
		const prefix = "Template:角色数据表/"
		if !strings.HasPrefix(page.Title, prefix) || strings.HasSuffix(page.Title, "/core") {
			continue
		}
		name := strings.TrimPrefix(page.Title, prefix)
		params := parseParams(page.Wikitext)

		id := nameToID[normalizeName(name)]
		if id == 0 {
			// 没有 charaId 就没法与上游对齐,产出来也用不了。跳过并不静默——
			// 数量会体现在最终条数上,与 Wiki 的 253 一对就知道漏了多少。
			continue
		}
		attr, ok := attributes[strings.TrimSpace(params["属性"])]
		if !ok {
			continue
		}

		c := masterdata.Chara{
			ID:         id,
			NameZh:     name,
			Attribute:  masterdata.Attribute(attr),
			GrowthType: strings.TrimSpace(params["成长类型"]),
			Disks:      strings.TrimSpace(params["盘"]),
			ByRarity:   map[int]masterdata.Stats{},
			Magia:      map[int][]masterdata.Art{},
		}
		for k, v := range params {
			m := rarityRe.FindStringSubmatch(k)
			if m == nil {
				continue
			}
			rarity, _ := strconv.Atoi(m[3])
			n := firstInt(v)
			if n < 0 {
				continue
			}
			st := c.ByRarity[rarity]
			switch m[1] + m[2] {
			case "HP最小":
				st.HPMin = n
			case "HP最大":
				st.HPMax = n
			case "ATK最小":
				st.ATKMin = n
			case "ATK最大":
				st.ATKMax = n
			case "DEF最小":
				st.DEFMin = n
			case "DEF最大":
				st.DEFMax = n
			}
			c.ByRarity[rarity] = st
		}
		if len(c.ByRarity) == 0 {
			continue
		}
		for rarity := range c.ByRarity {
			key := fmt.Sprintf("magia效果详细%d", rarity)
			if arts := parseArts(params[key], effects, gaps); len(arts) > 0 {
				c.Magia[rarity] = arts
			}
		}
		c.Doppel = parseArts(params["doppel效果详细"], effects, gaps)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func extractMemoria(path string, effects effectMap, gaps gapSet) ([]masterdata.Memoria, error) {
	var raw map[string]struct {
		Number      json.Number `json:"number"`
		NameZh      string      `json:"name_zh"`
		Rarity      json.Number `json:"rarity"`
		HPMin       int         `json:"hp_min"`
		HPMax       int         `json:"hp_max"`
		ATKMin      int         `json:"atk_min"`
		ATKMax      int         `json:"atk_max"`
		DEFMin      int         `json:"def_min"`
		DEFMax      int         `json:"def_max"`
		Cooldown    int         `json:"cooldown"`
		CooldownMax int         `json:"cooldown_max"`
		Detail      string      `json:"effect_detail"`
	}
	if err := readJSON(path, &raw); err != nil {
		return nil, err
	}
	var out []masterdata.Memoria
	seen := map[int]bool{}
	for _, m := range raw {
		num, err := m.Number.Int64()
		if err != nil || num <= 0 || seen[int(num)] {
			// 同号记忆结晶在 Wiki 里成对保留(客户端补全与原记录),取先到的那条。
			continue
		}
		seen[int(num)] = true
		rarity, _ := m.Rarity.Int64()
		out = append(out, masterdata.Memoria{
			Number: int(num),
			NameZh: m.NameZh,
			Rarity: int(rarity),
			Stats: masterdata.Stats{
				HPMin: m.HPMin, HPMax: m.HPMax,
				ATKMin: m.ATKMin, ATKMax: m.ATKMax,
				DEFMin: m.DEFMin, DEFMax: m.DEFMax,
			},
			Cooldown:    m.Cooldown,
			CooldownMax: m.CooldownMax,
			Effects:     parseArts(m.Detail, effects, gaps),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out, nil
}

// parseArts 把一行中文效果描述拆成结构化的 Art 列表。
//
// 未映射的效果名记进 gaps 并**跳过该条**——最终由调用方决定是报错还是只报告。
func parseArts(detail string, effects effectMap, gaps gapSet) []masterdata.Art {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return nil
	}
	var out []masterdata.Art
	// 一行可能有多条效果,用 <br> 分隔。
	for _, chunk := range strings.Split(detail, "<br>") {
		for _, m := range segRe.FindAllStringSubmatch(chunk, -1) {
			target, ok := targets[strings.TrimSpace(m[1])]
			if !ok {
				continue
			}
			// 查表前归一化:Wiki 里同一个效果会写成 "Accele MPUP" 与 "AcceleMPUP"
			// 两种。空格差异是转录噪声,不该让映射表为它多存一条。
			name := strings.TrimSpace(m[2])
			mapped, ok := effects[normalizeName(name)]
			if !ok {
				gaps.record(name, chunk)
				continue
			}
			out = append(out, masterdata.Art{
				Code:      mapped.Code,
				Target:    target,
				Sub:       mapped.Sub,
				Effect:    labelledInt(chunk, "效果值"),
				GrowPoint: labelledInt(chunk, "成长"),
				Rate:      labelledInt(chunk, "发动率"),
				Turns:     labelledInt(chunk, "持续回合"),
			})
		}
	}
	return out
}

// gapSet 记录未映射的效果名及其出现频次与一条真实例句。
//
// 例句是给填表的人看的:光有"魅惑"这个名字判断不出它该映射到哪个 code,
// 但看到"（全体）魅惑，发动率：40%，持续回合：1"就清楚多了——带发动率与持续回合,
// 说明是状态类而不是伤害类。
type gapSet map[string]*gapInfo

type gapInfo struct {
	Count  int
	Sample string
}

func (g gapSet) record(name, context string) {
	e := g[name]
	if e == nil {
		e = &gapInfo{}
		g[name] = e
	}
	e.Count++
	// 只留第一条例句。多留几条对判断没有额外帮助,却会让清单难读。
	if e.Sample == "" {
		e.Sample = strings.TrimSpace(context)
	}
}

type gapRow struct {
	Name   string
	Count  int
	Sample string
}

// sorted 按频次降序返回;同频次按名字排序,让两次运行的输出可以直接 diff。
func (g gapSet) sorted() []gapRow {
	list := make([]gapRow, 0, len(g))
	for k, v := range g {
		list = append(list, gapRow{Name: k, Count: v.Count, Sample: v.Sample})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Count != list[j].Count {
			return list[i].Count > list[j].Count
		}
		return list[i].Name < list[j].Name
	})
	return list
}

func reportGaps(g gapSet) {
	list := g.sorted()
	fmt.Fprintf(os.Stderr, "未映射的效果名(%d 个,按出现频次降序):\n", len(list))
	for _, e := range list {
		fmt.Fprintf(os.Stderr, "  %5d  %s\n", e.Count, e.Name)
	}
}

// writeGapsCSV 把缺口清单写成 CSV,供交给领域研究人员填写。
//
// 带 UTF-8 BOM:没有它,Excel 打开中文 CSV 是乱码——而这份文件的读者恰恰是用
// Excel 打开它的人。
func writeGapsCSV(g gapSet, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString("\ufeff"); err != nil {
		return err
	}
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"效果名", "出现频次", "例句", "code", "sub", "备注"}); err != nil {
		return err
	}
	for _, e := range g.sorted() {
		if err := w.Write([]string{e.Name, strconv.Itoa(e.Count), e.Sample, "", "", ""}); err != nil {
			return err
		}
	}
	return w.Error()
}

// ── 小工具 ──────────────────────────────────────────────────────────────

func parseParams(wikitext string) map[string]string {
	out := map[string]string{}
	for _, m := range paramRe.FindAllStringSubmatch(wikitext, -1) {
		out[strings.TrimSpace(m[1])] = strings.TrimSpace(m[2])
	}
	return out
}

// labelledInt 取 "标签：123" 里的数字;找不到返回 0。
func labelledInt(s, label string) int {
	i := strings.Index(s, label)
	if i < 0 {
		return 0
	}
	return firstIntOrZero(s[i+len(label):])
}

func firstIntOrZero(s string) int {
	if n := firstInt(s); n > 0 {
		return n
	}
	return 0
}

func firstInt(s string) int {
	m := numRe.FindString(s)
	if m == "" {
		return -1
	}
	n, err := strconv.Atoi(m)
	if err != nil {
		return -1
	}
	return n
}

// normalizeName 去掉名字里的空白,让"七海 やちよ"与"七海やちよ"能对上。
func normalizeName(s string) string {
	return strings.NewReplacer(" ", "", "　", "", "\t", "").Replace(strings.TrimSpace(s))
}

func readJSON(path string, dst any) error {
	blob, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(blob, dst)
}
