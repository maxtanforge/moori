package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
)

type sealedComposeDraft struct {
	Ciphertext []byte `json:"sealed_compose_draft"`
}

func (s *Store) sealComposeDraft(id string, raw []byte) ([]byte, error) {
	sealer, err := s.composeDraftCipher()
	if err != nil {
		return nil, err
	}
	return json.Marshal(sealedComposeDraft{Ciphertext: sealer.Seal(nil, nil, raw, []byte(id))})
}

func (s *Store) openComposeDraft(id string, raw []byte) ([]byte, error) {
	var draft sealedComposeDraft
	if err := json.Unmarshal(raw, &draft); err != nil {
		return nil, err
	}
	sealer, err := s.composeDraftCipher()
	if err != nil {
		return nil, err
	}
	return sealer.Open(nil, nil, draft.Ciphertext, []byte(id))
}

// Drafts are delivery data. A control restart discards the process key so a
// restored or interrupted operation must be previewed and submitted again.
func (s *Store) composeDraftCipher() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.composeDraftKey[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}
