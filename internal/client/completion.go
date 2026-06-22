package client

import (
	"fmt"
	"os"
)

// Completion prints a shell completion script for the given shell to stdout.
// Supported shells: bash, zsh, fish. Returns an error on unknown shells.
//
// Users install with one of:
//   source <(reminal completion bash)                  # bash, current session
//   reminal completion bash >> ~/.bashrc               # bash, persistent
//   reminal completion zsh > "${fpath[1]}/_reminal"    # zsh, persistent
//   reminal completion fish > ~/.config/fish/completions/reminal.fish
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

    local subcommands="connect attach stop relay version upgrade info qr doctor completion help"
    local flags="--connect --pin --verbose -v"

    case "${prev}" in
        completion)
            COMPREPLY=( $(compgen -W "bash zsh fish" -- "${cur}") )
            return 0
            ;;
        --connect|--pin)
            # No completions for session IDs / PINs (random per session).
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
        'connect:Connect to a remote session (positional <id-or-url> [pin])'
        'attach:Re-connect to the agent running on this machine'
        'stop:Stop broadcasting (keeps the local shell running)'
        'relay:Start a local relay server (dev only)'
        'version:Print version'
        'upgrade:Upgrade to the latest release'
        'info:Reprint session ID / PIN / URL / QR for the running agent'
        'qr:Print just the join QR for the running agent'
        'doctor:Run a self-diagnostic'
        'completion:Generate shell completion script'
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
                '(-v --verbose)'{-v,--verbose}'[Verbose mode]'
            ;;
        args)
            case "$words[1]" in
                completion)
                    _values 'shell' 'bash' 'zsh' 'fish'
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
complete -c reminal -n '__fish_use_subcommand' -a 'connect'    -d 'Connect to a remote session (positional <id-or-url> [pin])'
complete -c reminal -n '__fish_use_subcommand' -a 'attach'     -d 'Re-connect to the agent running on this machine'
complete -c reminal -n '__fish_use_subcommand' -a 'stop'       -d 'Stop broadcasting (keeps the local shell running)'
complete -c reminal -n '__fish_use_subcommand' -a 'relay'      -d 'Start a local relay server (dev only)'
complete -c reminal -n '__fish_use_subcommand' -a 'version'    -d 'Print version'
complete -c reminal -n '__fish_use_subcommand' -a 'upgrade'    -d 'Upgrade to the latest release'
complete -c reminal -n '__fish_use_subcommand' -a 'info'       -d 'Reprint session info for the running agent'
complete -c reminal -n '__fish_use_subcommand' -a 'qr'         -d 'Print just the join QR for the running agent'
complete -c reminal -n '__fish_use_subcommand' -a 'doctor'     -d 'Run a self-diagnostic'
complete -c reminal -n '__fish_use_subcommand' -a 'completion' -d 'Generate shell completion script'
complete -c reminal -n '__fish_use_subcommand' -a 'help'       -d 'Show help'

# Flags.
complete -c reminal -l connect -d 'Session ID or full relay URL'
complete -c reminal -l pin     -d 'PIN for the remote session'
complete -c reminal -s v -l verbose -d 'Verbose mode'

# completion <shell> args.
complete -c reminal -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'
`
