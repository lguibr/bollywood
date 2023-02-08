package actor

import "github.com/lguibr/bollywood/mailbox"

type Actor struct {
	Mailbox     *mailbox.Mailbox
	Performance func()
	Alive       bool
}

func NewActor(mailboxConfig []mailbox.MailboxConfig, performance func()) *Actor {
	return &Actor{
		Mailbox:     mailbox.NewMailbox(mailboxConfig),
		Performance: performance,
		Alive:       true,
	}
}

func (a *Actor) Perform() {
	go func() {
		for a.Alive {
			a.Performance()
		}
	}()
}

func (a *Actor) Die() {
	a.Mailbox.Deactivate()
	a.Alive = false
}
