package agent

// ReconnectMCPServers uses the owning session's idle guard and refreshes every
// enabled server, including deferred servers, without changing AlwaysLoad.
func (a *Agent) ReconnectMCPServers() error {
	next, err := a.Configuration()
	if err != nil {
		return err
	}
	return a.UpdateConfigAndReconnect(next)
}
