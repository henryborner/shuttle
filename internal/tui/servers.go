package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/henryborner/shuttle/internal/config"
	"github.com/henryborner/shuttle/internal/i18n"
	"github.com/henryborner/shuttle/internal/util"
	"golang.org/x/crypto/ssh"

	tea "github.com/charmbracelet/bubbletea"
)

type testStatus int

const (
	testNone testStatus = iota
	testTesting
	testOK
	testFail
)

// testResultMsg 异步测试结果消息
type testResultMsg struct {
	ok       bool
	msg      string
	osName   string
	hasAgent bool // shuttle binary found on remote
}

// deployResultMsg 异步部署结果消息
type deployResultMsg struct {
	ok  bool
	msg string
}

type serversModel struct {
	cfg     *config.Config
	servers []config.Server
	cursor  int
	cfgPath string
	adding  bool
	editIdx int
	// delete confirmation
	deleteIdx int // -1 = no pending
	updateIdx int // -1 = no pending
	// form
	formHost, formUser, formKey, formPortStr, formPass string
	formName                                           string
	formField                                          int
	// test & deploy
	testStatus testStatus
	testMsg    string
	deployed   bool
	hasAgent   bool // shuttle binary exists on remote
	// protect editing
	protectMode     bool
	protectCursor   int
	protectSrvIdx   int
	protectPatterns []string
	protectAdding   bool
	protectInput    string
	protectSaved    bool           // 保存成功提示
	remoteBrowser   *RemoteBrowser // Tab 呼出远端文件选择器
}

func newServers(cfg *config.Config, cfgPath string) *serversModel {
	return &serversModel{cfg: cfg, servers: cfg.Servers, cfgPath: cfgPath, formPortStr: "22", deleteIdx: -1, updateIdx: -1}
}

func (m *serversModel) Init() tea.Cmd { return nil }

func (m *serversModel) Update(msg tea.Msg) (serversModel, tea.Cmd) {
	// Protect editing mode — handle before all other modes
	if m.protectMode {
		return m.protectUpdate(msg)
	}

	// Update confirmation pending.
	if m.updateIdx >= 0 {
		key, ok := msg.(tea.KeyMsg)
		if !ok {
			return *m, nil
		}
		switch key.String() {
		case "y", "enter":
			if m.updateIdx < len(m.servers) {
				srv := m.servers[m.updateIdx]
				m.deployed = false
				m.testMsg = i18n.T("srv.updating")
				m.updateIdx = -1
				return *m, asyncUpdateAgent(srv)
			}
			m.updateIdx = -1
		case "n", "esc":
			m.updateIdx = -1
		}
		return *m, nil
	}

	// Delete confirmation pending.
	if m.deleteIdx >= 0 {
		// Silently eat async deploy results during confirmation
		if _, ok := msg.(deployResultMsg); ok {
			return *m, nil
		}
		key, ok := msg.(tea.KeyMsg)
		if !ok {
			return *m, nil
		}
		switch key.String() {
		case "y":
			if m.deleteIdx < len(m.servers) {
				srvName := m.servers[m.deleteIdx].Name
				m.servers = append(m.servers[:m.deleteIdx], m.servers[m.deleteIdx+1:]...)
				m.cursor = clamp(m.cursor, len(m.servers)-1)
				removeTasksForServer(m.cfg, srvName)
				m.saveConfig()
			}
			m.deleteIdx = -1
		case "d":
			if m.deleteIdx < len(m.servers) {
				srv := m.servers[m.deleteIdx]
				m.servers = append(m.servers[:m.deleteIdx], m.servers[m.deleteIdx+1:]...)
				m.cursor = clamp(m.cursor, len(m.servers)-1)
				removeTasksForServer(m.cfg, srv.Name)
				m.saveConfig()
				go tryRemoveRemoteAgent(srv)
			}
			m.deleteIdx = -1
		case "n", "esc":
			m.deleteIdx = -1
		}
		return *m, nil
	}

	if m.adding {
		return m.formUpdate(msg)
	}
	// Also handle async deploy results from list view
	if dr, ok := msg.(deployResultMsg); ok {
		m.deployed = dr.ok
		m.testMsg = dr.msg
		return *m, nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return *m, nil
	}
	switch key.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.servers)-1 {
			m.cursor++
		}
	case "a":
		m.resetForm()
	case "e", "enter":
		if m.cursor < len(m.servers) {
			m.adding = true
			m.editIdx = m.cursor
			s := m.servers[m.cursor]
			m.formName, m.formHost = s.Name, s.Host
			m.formUser, m.formKey, m.formPass = s.User, s.KeyFile, s.Pass
			m.formPortStr = fmt.Sprintf("%d", s.Port)
			m.formField = 0
			m.testStatus = testNone
			m.deployed = false
			m.hasAgent = false
			m.testMsg = StyleMuted.Render(i18n.T("srv.edit_mode"))
		}
	case "d":
		if m.cursor < len(m.servers) && len(m.servers) > 0 {
			m.deployed = false
			m.testMsg = ""
			m.deleteIdx = m.cursor
		}
	case "u":
		if m.cursor < len(m.servers) && len(m.servers) > 0 {
			m.updateIdx = m.cursor
		}
	case "p":
		if m.cursor < len(m.servers) && len(m.servers) > 0 {
			m.protectMode = true
			m.protectSrvIdx = m.cursor
			m.protectPatterns = append([]string{}, m.servers[m.cursor].Protect...)
			m.protectCursor = 0
			m.protectAdding = false
			m.protectInput = ""
		}
	}
	return *m, nil
}

func (m *serversModel) resetForm() {
	m.adding = true
	m.formName, m.formHost, m.formUser, m.formKey, m.formPass = "", "", "", "", ""
	m.formPortStr = "22"
	m.formField = 0
	m.testStatus = testNone
	m.testMsg = ""
	m.deployed = false
	m.hasAgent = false
	m.editIdx = -1
}

func (m *serversModel) formUpdate(msg tea.Msg) (serversModel, tea.Cmd) {
	// Handle async results
	if tr, ok := msg.(testResultMsg); ok {
		if tr.ok {
			m.testStatus = testOK
			m.hasAgent = tr.hasAgent
			agentInfo := ""
			if tr.hasAgent {
				agentInfo = " | Agent: " + IconOK
			} else {
				agentInfo = " | " + StyleWarning.Render(i18n.T("srv.no_agent"))
			}
			m.testMsg = fmt.Sprintf("%s %s  OS: %s%s", IconOK, i18n.T("srv.test_ok"), strings.TrimSpace(tr.osName), agentInfo)
		} else {
			m.testStatus = testFail
			m.testMsg = tr.msg
		}
		return *m, nil
	}
	if dr, ok := msg.(deployResultMsg); ok {
		m.deployed = dr.ok
		m.testMsg = dr.msg
		return *m, nil
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return *m, nil
	}
	switch key.String() {
	case "esc":
		m.adding = false
		m.testMsg = ""
	case "tab":
		m.formField = (m.formField + 1) % 6
	case "enter":
		if m.testStatus != testOK {
			m.testMsg = StyleWarning.Render(i18n.T("srv.must_test"))
			return *m, nil
		}
		if !m.hasAgent && !m.deployed {
			// No agent → try deploy
			m.testMsg = i18n.T("srv.deploying")
			authMethods := util.BuildAuthMethods(m.formKey, m.formPass)
			if len(authMethods) == 0 {
				m.testMsg = fmt.Sprintf("%s%s", i18n.T("srv.key_err"), i18n.T("srv.empty_auth"))
				return *m, nil
			}
			return *m, m.asyncDeploy(authMethods)
		}
		m.saveServer()
		m.saveConfig()
		m.adding = false
		m.testMsg = ""
	case "ctrl+t":
		m.testStatus = testTesting
		m.testMsg = i18n.T("srv.testing")
		authMethods := util.BuildAuthMethods(m.formKey, m.formPass)
		if len(authMethods) == 0 {
			m.testStatus = testFail
			m.testMsg = fmt.Sprintf("%s%s", i18n.T("srv.key_err"), i18n.T("srv.empty_auth"))
			return *m, nil
		}
		return *m, m.asyncTest(authMethods)
	case "backspace":
		m.backspaceField()
	default:
		if len(key.String()) == 1 && key.String()[0] >= 32 && key.String()[0] != 127 {
			m.appendField(key.String())
		}
	}
	return *m, nil
}

// asyncTest runs the connection test in a goroutine, returns result via message
func (m *serversModel) asyncTest(authMethods []ssh.AuthMethod) tea.Cmd {
	host := m.formHost
	port, _ := strconv.Atoi(m.formPortStr)
	if port <= 0 {
		port = 22
	}
	user := m.formUser

	return func() tea.Msg {
		cfg := &ssh.ClientConfig{
			User: user, Auth: authMethods,
			HostKeyCallback: util.CheckHostKey(), Timeout: 8 * time.Second,
		}
		client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", strings.TrimSpace(host), port), cfg)
		if err != nil {
			return testResultMsg{ok: false, msg: fmt.Sprintf(i18n.T("srv.connect_err"), err)}
		}
		defer client.Close()

		session, err := client.NewSession()
		if err != nil {
			return testResultMsg{ok: false, msg: fmt.Sprintf(i18n.T("srv.session_err"), err)}
		}
		out, err := session.Output("uname -s")
		session.Close()
		if err != nil {
			return testResultMsg{ok: false, msg: fmt.Sprintf(i18n.T("srv.os_err"), err)}
		}
		// Check if shuttle binary exists on remote
		hasAgent := false
		if s2, err := client.NewSession(); err == nil {
			_, err := s2.Output("shuttle version")
			hasAgent = (err == nil)
			s2.Close()
		}
		return testResultMsg{ok: true, msg: i18n.T("srv.test_ok"), osName: string(out), hasAgent: hasAgent}
	}
}

// asyncDeploy runs deployment in a goroutine
func (m *serversModel) asyncDeploy(authMethods []ssh.AuthMethod) tea.Cmd {
	host := m.formHost
	port, _ := strconv.Atoi(m.formPortStr)
	if port <= 0 {
		port = 22
	}
	user := m.formUser

	return func() tea.Msg {
		cfg := &ssh.ClientConfig{
			User: user, Auth: authMethods,
			HostKeyCallback: util.CheckHostKey(), Timeout: 15 * time.Second,
		}
		client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", strings.TrimSpace(host), port), cfg)
		if err != nil {
			return deployResultMsg{ok: false, msg: fmt.Sprintf(i18n.T("srv.deploy_err"), err)}
		}
		defer client.Close()

		exePath, _ := os.Executable()
		localBin := filepath.Join(filepath.Dir(exePath), "shuttle_linux")
		if _, err := os.Stat(localBin); os.IsNotExist(err) {
			// go run 可能不在项目目录，fallback 当前工作目录
			localBin = "shuttle_linux"
		}
		if _, err := os.Stat(localBin); os.IsNotExist(err) {
			return deployResultMsg{ok: false, msg: i18n.T("srv.not_found")}
		}
		binData, err := os.ReadFile(localBin)
		if err != nil {
			return deployResultMsg{ok: false, msg: fmt.Sprintf(i18n.T("srv.read_err"), err)}
		}

		// Try default system path first, then home dir as non-root fallback
		deployPaths := []struct {
			path string
			cmd  string
		}{
			{"/usr/local/bin/shuttle", "cat > /usr/local/bin/shuttle && chmod +x /usr/local/bin/shuttle"},
			{"$HOME/shuttle", "cat > $HOME/shuttle && chmod +x $HOME/shuttle && echo 'export PATH=$PATH:$HOME' >> $HOME/.bashrc"},
		}

		var lastErr error
		deployed := false
		for _, dp := range deployPaths {
			s, err := client.NewSession()
			if err != nil {
				lastErr = err
				continue
			}
			stdin, err := s.StdinPipe()
			if err != nil {
				lastErr = err
				s.Close()
				continue
			}
			s.Start(dp.cmd)
			stdin.Write(binData)
			stdin.Close()
			s.Wait()
			s.Close()

			// Verify
			v, err := client.NewSession()
			if err != nil {
				lastErr = err
				continue
			}
			out, err := v.Output(dp.path + " version")
			v.Close()
			if err != nil {
				lastErr = err
				continue
			}
			deployed = true
			return deployResultMsg{ok: true, msg: fmt.Sprintf("%s%s %s  (%s)", IconOK, i18n.T("srv.deployed"), string(out), dp.path)}
		}

		if !deployed {
			return deployResultMsg{ok: false, msg: fmt.Sprintf("%s: %v\n%s", i18n.T("srv.deploy_err"), lastErr, i18n.T("srv.manual_install"))}
		}
		return deployResultMsg{ok: false, msg: "unreachable"}
	}
}

func (m *serversModel) saveServer() {
	port, _ := strconv.Atoi(m.formPortStr)
	if port <= 0 {
		port = 22
	}
	s := config.Server{
		Name:    strings.TrimSpace(m.formName),
		Host:    strings.TrimSpace(m.formHost),
		Port:    port,
		User:    strings.TrimSpace(m.formUser),
		KeyFile: strings.TrimSpace(strings.TrimRight(m.formKey, "\x00")),
		Pass:    strings.TrimSpace(m.formPass),
	}
	if m.editIdx >= 0 && m.editIdx < len(m.servers) {
		s.Protect = m.servers[m.editIdx].Protect // 保留保护列表
		m.servers[m.editIdx] = s
		m.editIdx = -1
	} else {
		m.servers = append(m.servers, s)
	}
}

func (m *serversModel) appendField(ch string) {
	switch m.formField {
	case 0:
		m.formName += ch
	case 1:
		m.formHost += ch
	case 2:
		m.formPortStr += ch
	case 3:
		m.formUser += ch
	case 4:
		m.formKey += ch
	case 5:
		m.formPass += ch
	}
}

func (m *serversModel) backspaceField() {
	switch m.formField {
	case 0:
		if len(m.formName) > 0 {
			m.formName = m.formName[:len(m.formName)-1]
		}
	case 1:
		if len(m.formHost) > 0 {
			m.formHost = m.formHost[:len(m.formHost)-1]
		}
	case 2:
		if len(m.formPortStr) > 0 {
			m.formPortStr = m.formPortStr[:len(m.formPortStr)-1]
		}
	case 3:
		if len(m.formUser) > 0 {
			m.formUser = m.formUser[:len(m.formUser)-1]
		}
	case 4:
		if len(m.formKey) > 0 {
			m.formKey = m.formKey[:len(m.formKey)-1]
		}
	case 5:
		if len(m.formPass) > 0 {
			m.formPass = m.formPass[:len(m.formPass)-1]
		}
	}
}

func (m *serversModel) View(width, height int) string {
	if m.protectMode {
		return m.protectView(width, height)
	}
	if m.updateIdx >= 0 && m.updateIdx < len(m.servers) {
		srvName := m.servers[m.updateIdx].Name
		body := fmt.Sprintf("  %s\n\n  %s \"%s\"？\n\n  [Y] %s  [N] %s",
			StyleTitle.Render("⬆ "+i18n.T("srv.update_short")),
			StyleWarning.Render(i18n.T("srv.update_confirm")),
			StyleWarning.Render(srvName),
			StyleSuccess.Render(i18n.T("btn.yes")),
			StyleMuted.Render(i18n.T("btn.cancel")))
		return StyleBorder.Width(width - 4).Height(height - 2).Render(body)
	}
	if m.deleteIdx >= 0 && m.deleteIdx < len(m.servers) {
		srvName := m.servers[m.deleteIdx].Name
		body := fmt.Sprintf("  %s\n\n  %s \"%s\"？\n\n  [Y] %s\n  [D] %s\n  [N] %s",
			StyleTitle.Render("⚠ "+i18n.T("srv.delete")),
			StyleWarning.Render(i18n.T("map.delete_confirm")),
			StyleWarning.Render(srvName),
			StyleSuccess.Render(i18n.T("btn.yes")),
			StyleWarning.Render(i18n.T("srv.delete_agent")),
			StyleMuted.Render(i18n.T("btn.cancel")))
		return StyleBorder.Width(width - 4).Height(height - 2).Render(body)
	}
	if m.adding {
		return m.formView(width, height)
	}
	title := StyleTitle.Render("🖥  " + i18n.T("srv.title"))
	body := title + "\n\n"
	if len(m.servers) == 0 {
		body += "  " + StyleMuted.Render(i18n.T("help.empty")) + "\n"
	} else {
		for i, s := range m.servers {
			cur := "  "
			if i == m.cursor {
				cur = StyleInfo.Render("▸ ")
			}
			agent := ""
			body += fmt.Sprintf("%s%s  %s@%s:%d  %s%s\n",
				cur, s.Name, s.User, s.Host, s.Port,
				StyleMuted.Render("🔑 "+truncatePath(s.KeyFile, 20)), agent)
		}
	}
	if m.testMsg != "" {
		body += "\n  " + m.testMsg
	}
	body += "\n" + StyleMuted.Render("  "+i18n.T("help.add")+"  [E/Enter]"+i18n.T("srv.edit")+"  "+i18n.T("help.delete")+"  [U]"+i18n.T("srv.update_short")+"  [P]"+i18n.T("srv.protect")+"  "+i18n.T("help.nav"))
	return StyleBorder.Width(width - 4).Height(height - 2).Render(body)
}

func (m *serversModel) formView(width, height int) string {
	fields := []string{i18n.T("srv.field_name"), i18n.T("srv.field_host"), i18n.T("srv.field_port"), i18n.T("srv.field_user"), i18n.T("srv.field_key"), i18n.T("srv.field_pass")}
	hints := []string{
		i18n.T("srv.name_hint"), i18n.T("srv.host_hint"),
		i18n.T("srv.port_hint"), i18n.T("srv.user_hint"), i18n.T("srv.key_hint"),
		i18n.T("srv.pass_hint"),
	}
	// Mask password display
	passDisplay := m.formPass
	if passDisplay != "" {
		passDisplay = strings.Repeat("*", len(passDisplay))
	}
	vals := []string{m.formName, m.formHost, m.formPortStr, m.formUser, m.formKey, passDisplay}

	body := StyleTitle.Render("📝 "+i18n.T("srv.add")) + "\n\n"
	for i, f := range fields {
		prefix := "  "
		if i == m.formField {
			prefix = StyleInfo.Render("▸ ")
		}
		body += fmt.Sprintf("%s%s: %s\n", prefix, f, StyleWarning.Render(vals[i]))
		body += fmt.Sprintf("     %s\n", StyleMuted.Render(hints[i]))
	}

	// Test status area
	switch m.testStatus {
	case testTesting:
		body += "\n  " + StyleWarning.Render(m.testMsg)
	case testOK:
		body += "\n  " + m.testMsg
		if !m.hasAgent && !m.deployed {
			body += "\n  " + StyleWarning.Render("  "+i18n.T("srv.deploy_hint"))
		}
	case testFail:
		body += "\n  " + StyleDanger.Render(m.testMsg)
	default:
		if m.testMsg != "" {
			body += "\n  " + StyleWarning.Render(m.testMsg)
		}
	}

	body += "\n" + StyleMuted.Render("  [Ctrl+T] "+i18n.T("srv.test")+"  "+i18n.T("help.form"))

	return StyleBorder.Width(width - 4).Height(height - 2).Render(body)
}

func (m *serversModel) saveConfig() {
	m.cfg.Servers = m.servers
	saveConfig(m.cfg, m.cfgPath)
}

// removeTasksForServer removes all tasks that target a given server.
func removeTasksForServer(cfg *config.Config, serverName string) {
	filtered := cfg.Tasks[:0]
	for _, t := range cfg.Tasks {
		srv, _ := config.ParseTarget(t.Target)
		if srv != serverName {
			filtered = append(filtered, t)
		}
	}
	cfg.Tasks = filtered
}

// asyncUpdateAgent deploys shuttle_linux to the given server (standalone, no form needed).
func asyncUpdateAgent(srv config.Server) tea.Cmd {
	authMethods := util.BuildAuthMethods(srv.KeyFile, srv.Pass)
	if len(authMethods) == 0 {
		return func() tea.Msg {
			return deployResultMsg{ok: false, msg: i18n.T("srv.empty_auth")}
		}
	}
	port := srv.Port
	if port <= 0 {
		port = 22
	}
	return func() tea.Msg {
		cfg := &ssh.ClientConfig{
			User: srv.User, Auth: authMethods,
			HostKeyCallback: util.CheckHostKey(), Timeout: 15 * time.Second,
		}
		client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", strings.TrimSpace(srv.Host), port), cfg)
		if err != nil {
			return deployResultMsg{ok: false, msg: fmt.Sprintf(i18n.T("srv.deploy_err"), err)}
		}
		defer client.Close()

		exePath, _ := os.Executable()
		localBin := filepath.Join(filepath.Dir(exePath), "shuttle_linux")
		if _, err := os.Stat(localBin); os.IsNotExist(err) {
			localBin = "shuttle_linux"
		}
		if _, err := os.Stat(localBin); os.IsNotExist(err) {
			return deployResultMsg{ok: false, msg: i18n.T("srv.not_found")}
		}
		binData, err := os.ReadFile(localBin)
		if err != nil {
			return deployResultMsg{ok: false, msg: fmt.Sprintf(i18n.T("srv.read_err"), err)}
		}

		deployPaths := []struct {
			path string
			cmd  string
		}{
			{"/usr/local/bin/shuttle", "cat > /usr/local/bin/shuttle && chmod +x /usr/local/bin/shuttle"},
			{"$HOME/shuttle", "cat > $HOME/shuttle && chmod +x $HOME/shuttle && echo 'export PATH=$PATH:$HOME' >> $HOME/.bashrc"},
		}

		for _, dp := range deployPaths {
			s, _ := client.NewSession()
			if s == nil {
				continue
			}
			stdin, _ := s.StdinPipe()
			if stdin == nil {
				s.Close()
				continue
			}
			s.Start(dp.cmd)
			stdin.Write(binData)
			stdin.Close()
			s.Wait()
			s.Close()

			v, err := client.NewSession()
			if err != nil {
				continue
			}
			out, err := v.Output(dp.path + " version")
			v.Close()
			if err == nil {
				return deployResultMsg{ok: true, msg: fmt.Sprintf("%s%s %s  (%s)", IconOK, i18n.T("srv.deployed"), string(out), dp.path)}
			}
		}
		return deployResultMsg{ok: false, msg: i18n.T("srv.manual_install")}
	}
}

// protectUpdate handles key events in protect editing mode.
func (m *serversModel) protectUpdate(msg tea.Msg) (serversModel, tea.Cmd) {
	// Remote browser active — delegate to it
	if m.remoteBrowser != nil {
		m.remoteBrowser.Update(msg)
		if m.remoteBrowser.IsDone() {
			if !m.remoteBrowser.WasCancelled() {
				sel := m.remoteBrowser.SelectedPath()
				m.protectInput = sel
				m.protectAdding = true
			}
			m.remoteBrowser.Close()
			m.remoteBrowser = nil
		}
		return *m, nil
	}

	// Text input mode for adding a pattern
	if m.protectAdding {
		key, ok := msg.(tea.KeyMsg)
		if !ok {
			return *m, nil
		}
		switch key.String() {
		case "esc":
			m.protectAdding = false
			m.protectInput = ""
		case "tab":
			m.remoteBrowser = NewRemoteBrowser(m.servers[m.protectSrvIdx])
			return *m, nil
		case "enter":
			pat := strings.TrimSpace(m.protectInput)
			if pat != "" {
				m.protectPatterns = append(m.protectPatterns, pat)
				m.protectCursor = len(m.protectPatterns) - 1
				m.servers[m.protectSrvIdx].Protect = m.protectPatterns
				m.saveConfig()
				m.protectSaved = true
			}
			m.protectAdding = false
			m.protectInput = ""
		case "backspace":
			if len(m.protectInput) > 0 {
				m.protectInput = m.protectInput[:len(m.protectInput)-1]
			}
		default:
			if len(key.String()) == 1 && key.String()[0] >= 32 && key.String()[0] != 127 {
				m.protectInput += key.String()
			}
		}
		return *m, nil
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return *m, nil
	}
	switch key.String() {
	case "esc":
		m.protectMode = false
	case "up", "k":
		if m.protectCursor > 0 {
			m.protectCursor--
		}
		m.protectSaved = false
	case "down", "j":
		if m.protectCursor < len(m.protectPatterns)-1 {
			m.protectCursor++
		}
		m.protectSaved = false
	case "a":
		m.protectAdding = true
		m.protectInput = ""
		m.protectSaved = false
	case "d":
		if len(m.protectPatterns) > 0 && m.protectCursor < len(m.protectPatterns) {
			m.protectPatterns = append(m.protectPatterns[:m.protectCursor], m.protectPatterns[m.protectCursor+1:]...)
			if m.protectCursor >= len(m.protectPatterns) && m.protectCursor > 0 {
				m.protectCursor--
			}
			m.servers[m.protectSrvIdx].Protect = m.protectPatterns
			m.saveConfig()
		}
	}
	return *m, nil
}

// protectView renders the protect list editing view.
func (m *serversModel) protectView(width, height int) string {
	// Remote browser active — delegate rendering
	if m.remoteBrowser != nil {
		return m.remoteBrowser.View(width, height)
	}

	srvName := m.servers[m.protectSrvIdx].Name
	title := StyleTitle.Render(fmt.Sprintf(i18n.T("srv.protect_title"), srvName))
	body := title + "\n\n"

	if m.protectAdding {
		body += fmt.Sprintf("  %s\n  ▸ %s\n",
			StyleMuted.Render(i18n.T("srv.protect_input")),
			StyleWarning.Render(m.protectInput+"▌"))
		body += "\n" + StyleMuted.Render("  [Tab] "+i18n.T("browser.open")+"  [Enter] "+i18n.T("btn.save")+"  [Esc] "+i18n.T("btn.cancel"))
	} else {
		if m.protectSaved {
			body += "  " + StyleSuccess.Render("✓ "+i18n.T("srv.protect_saved")) + "\n\n"
			m.protectSaved = false // 一闪而过，下次按键清掉
		}
		if len(m.protectPatterns) == 0 {
			body += "  " + StyleMuted.Render(i18n.T("srv.protect_empty")) + "\n"
		} else {
			for i, p := range m.protectPatterns {
				cur := "  "
				if i == m.protectCursor {
					cur = StyleInfo.Render("▸ ")
				}
				body += fmt.Sprintf("%s🛡 %s\n", cur, StyleWarning.Render(p))
			}
		}
		body += "\n" + StyleMuted.Render("  [A] "+i18n.T("srv.protect_add")+"  [D] "+i18n.T("srv.protect_delete")+"  [Esc] "+i18n.T("btn.back"))
	}

	return StyleBorder.Width(width - 4).Height(height - 2).Render(body)
}

// tryRemoveRemoteAgent attempts to SSH into the server and remove the shuttle binary.
func tryRemoveRemoteAgent(srv config.Server) {
	authMethods := util.BuildAuthMethods(srv.KeyFile, srv.Pass)
	if len(authMethods) == 0 {
		return
	}
	cfg := &ssh.ClientConfig{
		User: srv.User, Auth: authMethods,
		HostKeyCallback: util.CheckHostKey(), Timeout: 8 * time.Second,
	}
	addr := fmt.Sprintf("%s:%d", strings.TrimSpace(srv.Host), srv.Port)
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return
	}
	defer client.Close()
	session, _ := client.NewSession()
	if session != nil {
		session.Run("rm -f /usr/local/bin/shuttle ~/shuttle")
		session.Close()
	}
}
