package main

import (
	"log/slog"
	"net/rpc"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	irpc "github.com/shadowglass-xyz/prism/internal/rpc"
)

var (
	docStyle        = lipgloss.NewStyle().Padding(1, 2)
	unselectedStyle = lipgloss.NewStyle().Padding(0, 1)
	selectedStyle   = unselectedStyle.Foreground(lipgloss.Color("15")).Background(lipgloss.Color("99"))
)

type root struct {
	client     *rpc.Client
	containers []string
	claims     []string
	selected   int
}

var _ tea.Model = (*root)(nil)

type updateKnownContainers struct {
	containers []string
	claims     []string
}

func (r *root) updateRPCState() tea.Cmd {
	return func() tea.Msg {
		var agentState irpc.AgentStateReply
		err := r.client.Call("State.Agent", &irpc.AgentStateArgs{}, &agentState)
		if err != nil {
			slog.Error("rpc client call failed", slog.Any("err", err))
			panic(err)
		}
		containers := make([]string, 0, len(agentState.Containers))
		for container := range agentState.Containers {
			containers = append(containers, container)
		}
		claims := make([]string, 0, len(agentState.Claims))
		for claim := range agentState.Claims {
			claims = append(containers, claim)
		}
		return updateKnownContainers{containers: containers, claims: claims}
	}
}

func (r *root) deleteClaim(claim string) tea.Cmd {
	return func() tea.Msg {
		var newState irpc.RemoveClaimReply
		err := r.client.Call("State.RemoveClaim", &irpc.RemoveClaimArgs{
			Claim: claim,
		}, &newState)
		if err != nil {
			slog.Error("rpc client call failed", slog.Any("err", err))
			panic(err)
		}
		containers := make([]string, 0, len(newState.Containers))
		for container := range newState.Containers {
			containers = append(containers, container)
		}
		slices.Sort(containers)
		claims := make([]string, 0, len(newState.Claims))
		for claim := range newState.Claims {
			claims = append(containers, claim)
		}
		slices.Sort(claims)
		return updateKnownContainers{containers: containers, claims: claims}
	}
}

func (r *root) addClaim(claim string) tea.Cmd {
	return func() tea.Msg {
		var newState irpc.AddClaimReply
		err := r.client.Call("State.AddClaim", &irpc.AddClaimArgs{
			Claim: claim,
		}, &newState)
		if err != nil {
			slog.Error("rpc client call failed", slog.Any("err", err))
			panic(err)
		}
		containers := make([]string, 0, len(newState.Containers))
		for container := range newState.Containers {
			containers = append(containers, container)
		}
		slices.Sort(containers)
		claims := make([]string, 0, len(newState.Claims))
		for claim := range newState.Claims {
			claims = append(containers, claim)
		}
		slices.Sort(claims)
		return updateKnownContainers{containers: containers, claims: claims}
	}
}

func (r *root) Init() tea.Cmd {
	return r.updateRPCState()
}

func (r *root) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmds := make([]tea.Cmd, 1)
	switch msg := msg.(type) {
	case updateKnownContainers:
		r.containers = msg.containers
		r.claims = msg.claims
		cmds = append(cmds, tea.Tick(2*time.Second, func(time.Time) tea.Msg {
			return r.updateRPCState()
		}))
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return r, tea.Quit
		case "enter":
			// Send the choice on the channel and exit.
			// m.choice = choices[m.cursor]
			// return m, tea.Quit

		case "down", "j":
			r.selected++
			if r.selected >= len(r.containers) {
				r.selected = 0
			}

		case "up", "k":
			r.selected--
			if r.selected < 0 {
				r.selected = len(r.containers) - 1
			}
		case "d":
			return r, r.deleteClaim(r.containers[r.selected])
		case "c":
			return r, r.addClaim(r.containers[r.selected])
		}
	case tea.WindowSizeMsg:
	}

	return r, tea.Batch(cmds...)
}

// View implements tea.Model.
func (r *root) View() string {
	s := strings.Builder{}
	s.WriteString(docStyle.Render("Known Containers") + "\n")

	for i, c := range r.containers {
		style := unselectedStyle
		status := "[unclaimed]"
		if slices.Contains(r.claims, c) {
			status = "[claimed]  "
		}
		cString := strings.Builder{}
		if r.selected == i {
			style = selectedStyle
			cString.WriteString("(•) ")
		} else {
			cString.WriteString("( ) ")
		}
		cString.WriteString(c)
		cString.WriteString(" " + status + " ")
		s.WriteString(style.Render(cString.String()))
		s.WriteString("\n")
	}
	s.WriteString("\n(press q to quit; c to claim; d to delete claim)\n")

	return s.String()
}

func main() {
	client, err := rpc.Dial("tcp", "localhost:11245")
	if err != nil {
		slog.Error("dialing rpc server", slog.Any("err", err))
		panic(err)
	}

	r := root{
		client: client,
	}
	if _, err := tea.NewProgram(&r, tea.WithAltScreen()).Run(); err != nil {
		slog.Error("running program", slog.Any("err", err))
		panic(err)
	}
}
