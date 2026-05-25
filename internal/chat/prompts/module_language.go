package prompts

// LanguageRule returns the language directive injected into all chat kinds.
//
// Behaviour by lang:
//
//	"zh" — always respond in Simplified Chinese (简体中文).
//	"en" — always respond in English.
//	""   — mirror the user's input language; default to Chinese when
//	        the language cannot be determined yet (historical behaviour).
func LanguageRule(lang string) string {
	switch lang {
	case "zh":
		return `## Language

始终使用**简体中文**回复。所有输出——分析、计划、评论及正文——均须以中文书写，无论用户的输入语言如何。代码标识符、日志片段及机器可读标记（如 ` + "`<!-- clawflow:outcome=… -->`" + `）保持不变。`
	case "en":
		return `## Language

Always respond in **English**. All output — analysis, plans, comments, and prose — must be written in English regardless of the input language.`
	default:
		return `## Language

Match the user's input language. If you can't tell yet, default to
Chinese (本项目用户母语为中文，但请尊重用户实际输入的语言).`
	}
}
