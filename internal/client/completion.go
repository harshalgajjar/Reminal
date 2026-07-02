// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"fmt"
	"os"
	"strings"

	"github.com/reminal/reminal/internal/session"
)

// CompleteSessions prints completion candidates for the verbs that take a
// session id/name/port (attach, kill, stop, info, qr, rename). Each line is
// "value<TAB>description": both the id and the name (when set) of every live
// shell session, plus each port forward. The generated shell scripts call this
// via the hidden `reminal __complete`. Best-effort and silent — any error just
// yields no candidates so completion degrades gracefully.
func CompleteSessions() {
	all, err := session.ReadAllActive()
	if err != nil {
		return
	}
	// Descriptions must stay on one line and avoid the tab field separator so
	// all three shells parse "value<TAB>desc" correctly.
	clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace
	for _, a := range all {
		if a.IsPort() {
			fmt.Printf("%d\tport forward to localhost:%d\n", a.Port, a.Port)
			continue
		}
		mode := "foreground"
		if a.Headless {
			mode = "headless"
		}
		// id candidate (described by name+mode) and, when named, a name
		// candidate (described by id+mode), so either can be completed.
		if a.Name != "" {
			fmt.Printf("%s\t%s\n", a.ID, clean(a.Name)+" "+mode)
			fmt.Printf("%s\t%s\n", clean(a.Name), a.ID+" "+mode)
		} else {
			fmt.Printf("%s\t%s\n", a.ID, mode)
		}
	}
}

// Completion prints a shell completion script for the given shell to stdout.
// Supported shells: bash, zsh, fish. Returns an error on unknown shells.
//
// Users install with one of:
//
//	source <(reminal completion bash)                  # bash, current session
//	reminal completion bash >> ~/.bashrc               # bash, persistent
//	reminal completion zsh > "${fpath[1]}/_reminal"    # zsh, persistent
//	reminal completion fish > ~/.config/fish/completions/reminal.fish
func Completion(shell string) error {
	switch shell {
	case "bash":
		fmt.Print(bashCompletion)
	case "zsh":
		fmt.Print(zshCompletion)
	case "fish":
		fmt.Print(fishCompletion)
	default:
		fmt.Fprintf(os.Stderr, "unsupported shell: %q (try bash, zsh, or fish)\n", shell)
		return fmt.Errorf("unsupported shell: %q", shell)
	}
	return nil
}

const bashCompletion = `# reminal bash completion
_reminal_complete() {
    local cur prev words cword
    _init_completion 2>/dev/null || {
        cur="${COMP_WORDS[COMP_CWORD]}"
        prev="${COMP_WORDS[COMP_CWORD-1]}"
        words=("${COMP_WORDS[@]}")
        cword=$COMP_CWORD
    }

    local subcommands="connect new attach list ls kill stop rename prune restart expose send copy paste notify connections info qr doctor completion upgrade relay version help"
    local flags="--connect --pin --name --verbose -v"

    case "${prev}" in
        completion)
            COMPREPLY=( $(compgen -W "bash zsh fish" -- "${cur}") )
            return 0
            ;;
        attach|kill|stop|rename|info|qr)
            # Complete local session ids/names (and ports for stop).
            COMPREPLY=( $(compgen -W "$(reminal __complete 2>/dev/null | cut -f1)" -- "${cur}") )
            return 0
            ;;
        --connect|--pin)
            # No completions for remote session IDs / PINs (random per session).
            return 0
            ;;
    esac

    if [[ ${cword} -eq 1 ]]; then
        COMPREPLY=( $(compgen -W "${subcommands} ${flags}" -- "${cur}") )
        return 0
    fi

    if [[ "${cur}" == -* ]]; then
        COMPREPLY=( $(compgen -W "${flags}" -- "${cur}") )
        return 0
    fi
}
complete -F _reminal_complete reminal
`

const zshCompletion = `# reminal zsh completion
# Source this in your .zshrc, or save as _reminal anywhere in $fpath and
# run compinit. compdef takes effect immediately when sourced.
_reminal() {
    local -a subcommands
    subcommands=(
        'connect:Connect to a remote session (<id-or-url> [pin])'
        'new:Spawn a fresh background session'
        'attach:Re-connect to a local session (id/name; no arg = picker)'
        'list:List sessions on this machine'
        'ls:List sessions on this machine'
        'kill:Fully terminate a session (id/name)'
        'stop:Stop broadcasting (id/name/port)'
        'rename:Rename a session (id/name then new name)'
        'prune:Kill idle, unwatched sessions'
        'restart:Hot-restart the agent into the latest binary'
        'expose:Forward a local HTTP port'
        'send:Push a file to every connected viewer'
        'copy:Offer a file for pickup (one-time code)'
        'paste:Fetch a file offered by reminal copy'
        'notify:Push a notification to viewers'
        'connections:List currently attached viewers'
        'info:Show session info (id/name)'
        'qr:Print the join QR (id/name)'
        'doctor:Run a self-diagnostic'
        'completion:Generate shell completion script'
        'upgrade:Upgrade to the latest release'
        'relay:Start a local relay server (dev only)'
        'version:Print version'
        'help:Show help'
    )

    _arguments -C \
        '1: :->cmds' \
        '*::arg:->args'

    case "$state" in
        cmds)
            _describe -t commands 'reminal commands' subcommands
            _arguments \
                '--connect[Session ID or full relay URL]:session' \
                '--pin[PIN for the remote session]:pin' \
                '--name[Name for this session]:name' \
                '(-v --verbose)'{-v,--verbose}'[Verbose mode]'
            ;;
        args)
            case "$words[1]" in
                completion)
                    _values 'shell' 'bash' 'zsh' 'fish'
                    ;;
                attach|kill|stop|rename|info|qr)
                    local -a sess
                    local line
                    for line in ${(f)"$(reminal __complete 2>/dev/null)"}; do
                        sess+=("${line%%$'\t'*}:${line#*$'\t'}")
                    done
                    _describe -t sessions 'session' sess
                    ;;
            esac
            ;;
    esac
}

compdef _reminal reminal
`

const fishCompletion = `# reminal fish completion
complete -c reminal -f

# Subcommands (only valid as the first non-flag argument).
complete -c reminal -n '__fish_use_subcommand' -a 'connect'     -d 'Connect to a remote session (<id-or-url> [pin])'
complete -c reminal -n '__fish_use_subcommand' -a 'new'         -d 'Spawn a fresh background session'
complete -c reminal -n '__fish_use_subcommand' -a 'attach'      -d 'Re-connect to a local session (id/name; no arg = picker)'
complete -c reminal -n '__fish_use_subcommand' -a 'list'        -d 'List sessions on this machine'
complete -c reminal -n '__fish_use_subcommand' -a 'ls'          -d 'List sessions on this machine'
complete -c reminal -n '__fish_use_subcommand' -a 'kill'        -d 'Fully terminate a session (id/name)'
complete -c reminal -n '__fish_use_subcommand' -a 'stop'        -d 'Stop broadcasting (id/name/port)'
complete -c reminal -n '__fish_use_subcommand' -a 'rename'      -d 'Rename a session (id/name then new name)'
complete -c reminal -n '__fish_use_subcommand' -a 'prune'       -d 'Kill idle, unwatched sessions'
complete -c reminal -n '__fish_use_subcommand' -a 'restart'     -d 'Hot-restart the agent into the latest binary'
complete -c reminal -n '__fish_use_subcommand' -a 'expose'      -d 'Forward a local HTTP port'
complete -c reminal -n '__fish_use_subcommand' -a 'send'        -d 'Push a file to every connected viewer'
complete -c reminal -n '__fish_use_subcommand' -a 'copy'        -d 'Offer a file for pickup (one-time code)'
complete -c reminal -n '__fish_use_subcommand' -a 'paste'       -d 'Fetch a file offered by reminal copy'
complete -c reminal -n '__fish_use_subcommand' -a 'notify'      -d 'Push a notification message to every connected viewer'
complete -c reminal -n '__fish_use_subcommand' -a 'connections' -d 'List currently attached viewers'
complete -c reminal -n '__fish_use_subcommand' -a 'info'        -d 'Show session info (id/name)'
complete -c reminal -n '__fish_use_subcommand' -a 'qr'          -d 'Print the join QR (id/name)'
complete -c reminal -n '__fish_use_subcommand' -a 'doctor'      -d 'Run a self-diagnostic'
complete -c reminal -n '__fish_use_subcommand' -a 'completion'  -d 'Generate shell completion script'
complete -c reminal -n '__fish_use_subcommand' -a 'upgrade'     -d 'Upgrade to the latest release'
complete -c reminal -n '__fish_use_subcommand' -a 'relay'       -d 'Start a local relay server (dev only)'
complete -c reminal -n '__fish_use_subcommand' -a 'version'     -d 'Print version'
complete -c reminal -n '__fish_use_subcommand' -a 'help'        -d 'Show help'

# Flags.
complete -c reminal -l connect -d 'Session ID or full relay URL'
complete -c reminal -l pin     -d 'PIN for the remote session'
complete -c reminal -l name    -d 'Name for this session'
complete -c reminal -s v -l verbose -d 'Verbose mode'

# completion <shell> args.
complete -c reminal -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'

# Live session id/name (and port) completion for the verbs that take one.
# 'reminal __complete' prints "value<tab>description" lines, which fish maps
# directly to completion candidates with descriptions.
complete -c reminal -f -n '__fish_seen_subcommand_from attach kill stop rename info qr' -a '(reminal __complete)'
`
