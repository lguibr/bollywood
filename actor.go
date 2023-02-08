package main

type Actor struct {
	Mailbox     *Mailbox
	Performance func()
	Alive       bool
}

func NewActor(mailboxConfig []MailboxConfig, performance func()) *Actor {
	return &Actor{
		Mailbox:     NewMailbox(mailboxConfig),
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
