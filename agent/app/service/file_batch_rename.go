package service

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/1Panel-dev/1Panel/agent/app/dto/request"
	"github.com/1Panel-dev/1Panel/agent/app/dto/response"
	"github.com/1Panel-dev/1Panel/agent/utils/files"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// defaultEpisodePatterns are tried in order against the file stem.
// The first capture group holds the episode number.
var defaultEpisodePatterns = []string{
	`第\s*(\d{1,4})\s*集`,                                       // 第01集 / 第 1 集
	`[Ss]\d{1,2}[\s._\-]*[Ee](\d{1,4})`,                       // S01E02
	`(?:^|[\s._\[(])[Ee][Pp]?[_\- ]?(\d{1,4})(?:[\s._\])]|$)`, // EP01 / E01
	`\[(\d{1,4})(?:\s*[vV]\d+)?]`,                             // [01] / [01v2]
	`-\s*(\d{1,4})(?:[\s._\[(]|$)`,                            // "Show - 01 ..."
}

var (
	batchRenameTemplateToken = regexp.MustCompile(`\{(\w+)}`)
	batchRenameControlChars  = regexp.MustCompile(`[\x00-\x1f/]`)
	titleCaser               = cases.Title(language.Und)
)

func (f *FileService) BatchRenamePreview(req request.FileBatchRenamePreview) ([]response.BatchRenameItem, error) {
	return f.computeBatchRename(req.Paths, req.Rule)
}

func (f *FileService) BatchRename(req request.FileBatchRename) error {
	items, err := f.computeBatchRename(req.Paths, req.Rule)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Error != "" {
			return fmt.Errorf("%s: %s", item.OldName, item.Error)
		}
	}
	fo := files.NewFileOp()
	for _, item := range items {
		if !item.Changed {
			continue
		}
		if err := fo.Rename(item.OldPath, item.NewPath); err != nil {
			return err
		}
	}
	return nil
}

func (f *FileService) computeBatchRename(paths []string, rule request.BatchRenameRule) ([]response.BatchRenameItem, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no file selected")
	}
	dir := filepath.Dir(paths[0])
	for _, p := range paths[1:] {
		if filepath.Dir(p) != dir {
			return nil, fmt.Errorf("batch rename only supports files in the same directory")
		}
	}
	ruleApplier, err := newBatchRuleApplier(rule, len(paths))
	if err != nil {
		return nil, err
	}

	items := make([]response.BatchRenameItem, 0, len(paths))
	oldPathSet := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		oldPathSet[p] = struct{}{}
	}
	usedNewNames := make(map[string]struct{}, len(paths))
	fo := files.NewFileOp()

	for i, p := range paths {
		item := response.BatchRenameItem{OldPath: p, OldName: filepath.Base(p)}
		stem, ext := splitStemExt(item.OldName)
		newStem, applyErr := ruleApplier(stem, i)
		if applyErr != "" {
			item.Error = applyErr
			items = append(items, item)
			continue
		}
		newName := newStem + ext
		item.NewName = newName
		item.NewPath = filepath.Join(dir, newName)
		item.Changed = newName != item.OldName

		if errMsg := validateNewName(newStem); errMsg != "" {
			item.Error = errMsg
			items = append(items, item)
			continue
		}
		if item.Changed {
			if _, ok := usedNewNames[newName]; ok {
				item.Error = "target name conflicts with another item in this batch"
				items = append(items, item)
				continue
			}
			if _, targetIsSelected := oldPathSet[item.NewPath]; targetIsSelected {
				item.Error = "target name belongs to another selected file"
				items = append(items, item)
				continue
			}
			if fo.Stat(item.NewPath) {
				item.Error = "target file already exists"
				items = append(items, item)
				continue
			}
			usedNewNames[newName] = struct{}{}
		}
		items = append(items, item)
	}
	return items, nil
}

func validateNewName(stem string) string {
	if strings.TrimSpace(stem) == "" {
		return "new name must not be empty"
	}
	if stem == "." || stem == ".." {
		return "invalid file name"
	}
	if batchRenameControlChars.MatchString(stem) {
		return "file name contains invalid characters"
	}
	if files.IsInvalidChar(stem) {
		return "file name contains invalid characters"
	}
	return ""
}

// splitStemExt splits a base name into stem and extension.
// Dotfiles (e.g. ".bashrc") are treated as having no extension.
func splitStemExt(base string) (string, string) {
	ext := filepath.Ext(base)
	if ext != "" {
		return strings.TrimSuffix(base, ext), ext
	}
	return base, ""
}

type batchRuleApplier func(stem string, index int) (string, string)

func newBatchRuleApplier(rule request.BatchRenameRule, total int) (batchRuleApplier, error) {
	step := rule.Step
	if step == 0 {
		step = 1
	}
	pad := func(value int) string {
		if rule.Padding <= 0 {
			return strconv.Itoa(value)
		}
		return fmt.Sprintf("%0*d", rule.Padding, value)
	}
	render := func(stem string, values map[string]string) string {
		return batchRenameTemplateToken.ReplaceAllStringFunc(stem, func(token string) string {
			key := token[1 : len(token)-1]
			if v, ok := values[key]; ok {
				return v
			}
			return token
		})
	}

	switch rule.Type {
	case "replace":
		if rule.Find == "" {
			return nil, fmt.Errorf("find text must not be empty")
		}
		if rule.UseRegex {
			pattern := rule.Find
			if !rule.MatchCase {
				pattern = "(?i)" + pattern
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf("invalid regular expression: %v", err)
			}
			return func(stem string, _ int) (string, string) {
				return re.ReplaceAllString(stem, rule.Replace), ""
			}, nil
		}
		return func(stem string, _ int) (string, string) {
			return replaceLiteral(stem, rule.Find, rule.Replace, rule.MatchCase), ""
		}, nil
	case "insert":
		if rule.Text == "" {
			return nil, fmt.Errorf("insert text must not be empty")
		}
		return func(stem string, _ int) (string, string) {
			runes := []rune(stem)
			pos := rule.Position
			if pos < 0 || pos > len(runes) {
				pos = len(runes)
			}
			return string(runes[:pos]) + rule.Text + string(runes[pos:]), ""
		}, nil
	case "case":
		switch rule.CaseType {
		case "lower", "upper", "title":
		default:
			return nil, fmt.Errorf("unsupported case type: %s", rule.CaseType)
		}
		return func(stem string, _ int) (string, string) {
			switch rule.CaseType {
			case "lower":
				return strings.ToLower(stem), ""
			case "upper":
				return strings.ToUpper(stem), ""
			default:
				return titleCaser.String(strings.ToLower(stem)), ""
			}
		}, nil
	case "number":
		template := rule.Template
		if strings.TrimSpace(template) == "" {
			template = "{name}_{n}"
		}
		return func(stem string, index int) (string, string) {
			number := rule.Start + index*step
			return render(template, map[string]string{
				"name": stem,
				"text": rule.Text,
				"n":    pad(number),
				"ext":  "",
			}), ""
		}, nil
	case "episode":
		var custom *regexp.Regexp
		if strings.TrimSpace(rule.Pattern) != "" {
			re, err := regexp.Compile(rule.Pattern)
			if err != nil {
				return nil, fmt.Errorf("invalid episode regular expression: %v", err)
			}
			custom = re
		}
		builtIn := make([]*regexp.Regexp, 0, len(defaultEpisodePatterns))
		for _, p := range defaultEpisodePatterns {
			builtIn = append(builtIn, regexp.MustCompile(p))
		}
		template := rule.Template
		if strings.TrimSpace(template) == "" {
			if strings.TrimSpace(rule.Text) != "" {
				template = "{text} - E{episode}"
			} else {
				template = "{name} - E{episode}"
			}
		}
		return func(stem string, index int) (string, string) {
			episode, ok := extractEpisode(stem, custom, builtIn)
			if !ok {
				return "", "episode number not found in file name"
			}
			episode += rule.Offset
			if episode < 0 {
				return "", "episode number is negative after applying the offset"
			}
			return render(template, map[string]string{
				"name":    stem,
				"text":    rule.Text,
				"n":       pad(rule.Start + index*step),
				"episode": pad(episode),
				"ext":     "",
			}), ""
		}, nil
	default:
		return nil, fmt.Errorf("unsupported batch rename rule: %s", rule.Type)
	}
}

func extractEpisode(stem string, custom *regexp.Regexp, builtIn []*regexp.Regexp) (int, bool) {
	match := func(re *regexp.Regexp) (int, bool) {
		groups := re.FindStringSubmatch(stem)
		if len(groups) == 0 {
			return 0, false
		}
		raw := groups[0]
		if len(groups) > 1 {
			raw = groups[1]
		}
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return 0, false
		}
		return value, true
	}
	if custom != nil {
		if value, ok := match(custom); ok {
			return value, true
		}
	}
	for _, re := range builtIn {
		if value, ok := match(re); ok {
			return value, true
		}
	}
	return 0, false
}

func replaceLiteral(s, find, repl string, matchCase bool) string {
	if find == "" {
		return s
	}
	if matchCase {
		return strings.ReplaceAll(s, find, repl)
	}
	var builder strings.Builder
	lowerS := strings.ToLower(s)
	lowerFind := strings.ToLower(find)
	for {
		idx := strings.Index(lowerS, lowerFind)
		if idx < 0 {
			builder.WriteString(s)
			return builder.String()
		}
		builder.WriteString(s[:idx])
		builder.WriteString(repl)
		s = s[idx+len(find):]
		lowerS = lowerS[idx+len(find):]
	}
}
