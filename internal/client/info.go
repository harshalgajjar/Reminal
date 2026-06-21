package client

import (
	"fmt"
	"os"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/reminal/reminal/internal/session"
)

// ShowActiveInfo reprints the join details for the currently running agent,
// for users who lost the original banner (cleared the terminal, scrolled past,
// etc). Returns an error if no live agent is recorded. The active record is
// considered stale if its PID is no longer alive — those are pruned in
// session.ReadActive, so a successful return guarantees a real running agent.
func ShowActiveInfo() error {
	a, err := session.ReadActive()
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no active reminal session — start one with `reminal`")
		}
		return err
	}

	fmt.Println()
	fmt.Println("  reminal — remote terminal")
	fmt.Println()
	fmt.Printf("  Session:  %s\n", a.ID)
	fmt.Printf("  PIN:      %s\n", a.PIN)
	fmt.Printf("  Open:     %s\n", a.OpenURL)
	fmt.Printf("  Connect:  reminal connect %s %s\n", a.ID, a.PIN)
	fmt.Printf("  Started:  %s (PID %d)\n", a.StartedAt.Format(time.RFC3339), a.PID)
	fmt.Println()
	fmt.Println("  Scan to join from your phone:")
	fmt.Println()
	joinURL := fmt.Sprintf("%s#p=%s", a.OpenURL, a.PIN)
	qrterminal.GenerateHalfBlock(joinURL, qrterminal.L, os.Stdout)
	fmt.Println()
	return nil
}
