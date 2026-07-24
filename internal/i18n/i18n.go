package i18n

import "sync"

// Lang is the language type.
// Lang 语言类型。
type Lang string

const (
	EN Lang = "en"
	ZH Lang = "zh"
)

var (
	mu      sync.RWMutex
	current Lang = EN
	strings      = make(map[Lang]map[string]string)
)

// SetLang switches the current UI language.
// SetLang 切换当前界面语言。
func SetLang(l Lang) {
	mu.Lock()
	current = l
	mu.Unlock()
}

// Current returns the current UI language.
// Current 返回当前界面语言。
func Current() Lang {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// T translates a key to the current language. Returns the key itself if not found.
// T 根据当前语言翻译 key。未找到时返回 key 本身。
func T(key string) string {
	mu.RLock()
	defer mu.RUnlock()
	if m, ok := strings[current]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}

	if m, ok := strings[EN]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return key
}

// register 注册翻译（init 中调用）
func register(l Lang, entries map[string]string) {
	mu.Lock()
	defer mu.Unlock()
	if strings[l] == nil {
		strings[l] = make(map[string]string)
	}
	for k, v := range entries {
		strings[l][k] = v
	}
}
