package main

import "encoding/binary"

type Message struct {
	ID       uint64
	From     string
	UnixTime int64
	Type     uint64
	Text     string
}

const (
	MessageText  = 1
	MessageJoin  = 2
	MessageLeave = 3
)

func (m Message) Marshal() (out []byte) {
	out = binary.BigEndian.AppendUint64(out, m.ID)
	out = binary.BigEndian.AppendUint64(out, uint64(m.Type))
	out = binary.BigEndian.AppendUint64(out, uint64(m.UnixTime))
	out = binary.AppendUvarint(out, uint64(len(m.From)))
	out = append(out, m.From...)
	out = binary.AppendUvarint(out, uint64(len(m.Text)))
	out = append(out, m.Text...)
	return
}

func (m *Message) Unmarshal(p []byte) error {
	m.ID, p = binary.BigEndian.Uint64(p), p[8:]
	m.Type, p = binary.BigEndian.Uint64(p), p[8:]
	m.UnixTime, p = int64(binary.BigEndian.Uint64(p)), p[8:]

	tmp, w := binary.Uvarint(p)
	p = p[w:]
	m.From = string(p[:tmp])
	p = p[tmp:]

	tmp, w = binary.Uvarint(p)
	p = p[w:]
	m.Text = string(p[:tmp])
	p = p[tmp:]
	return nil
}
