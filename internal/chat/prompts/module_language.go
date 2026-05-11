package prompts

// LanguageRule returns the language-matching directive injected into
// all chat kinds.
//
// The rule: mirror the user's input language; default to Chinese when
// the language cannot be determined yet.
func LanguageRule() string {
	return `## Language

Match the user's input language. If you can't tell yet, default to
Chinese (本项目用户母语为中文，但请尊重用户实际输入的语言).`
}
