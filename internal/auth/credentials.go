package auth

// Credentials is a single configured access key pair.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
}

// Store maps access key ids to credentials.
type Store struct {
	creds map[string]Credentials
}

// NewStore builds a credential store.
func NewStore(creds []Credentials) *Store {
	m := make(map[string]Credentials, len(creds))
	for _, c := range creds {
		if c.AccessKeyID == "" {
			continue
		}
		m[c.AccessKeyID] = c
	}
	return &Store{creds: m}
}

// Lookup returns the credentials for an access key id.
func (s *Store) Lookup(accessKeyID string) (Credentials, bool) {
	c, ok := s.creds[accessKeyID]
	return c, ok
}

// Enabled reports whether authentication is configured.
func (s *Store) Enabled() bool { return len(s.creds) > 0 }
