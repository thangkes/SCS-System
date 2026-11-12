package cluster

import (
	"errors"
	"fmt"
)

// Bỏ phiếu cho quyết định kế tiếp (seq = State.Seq + 1).
//
// Ký một đề nghị = bỏ phiếu cho nó. Luật để hai quyết định khác nhau không thể cùng đủ chữ ký
// cho một seq (khi các node trung thực):
//
//  1. Ở mỗi (seq, round), node chỉ ký MỘT đề nghị (ký lại đúng đề nghị đó thì được).
//  2. Đã ký ở round r thì không ký round nhỏ hơn r nữa.
//  3. Ký ở round r > 1 chỉ khi có bằng chứng (các phiếu có chữ ký của round r-1) cho thấy ở round
//     r-1 không đề nghị nào có thể đủ chữ ký, kể cả khi mọi node chưa bỏ phiếu đều dồn cho nó.
//
// Node gian có thể ký hai đề nghị cùng (seq, round); khi thấy hai chữ ký như vậy của cùng một
// node, Equivocators trả về node đó làm bằng chứng (để cô lập).

// Votes: phiếu node này đã bỏ cho seq kế tiếp. Server lưu lại (file) để khởi động lại không ký
// trùng.
type Votes struct {
	Seq      uint64            `json:"seq"`
	ByRound  map[uint32]string `json:"by_round"` // round → mã băm đề nghị đã ký
	MaxRound uint32            `json:"max_round"`
}

// ErrVoted: node đã ký một đề nghị khác ở (seq, round) này, hoặc đã sang round sau.
var ErrVoted = errors.New("đã bỏ phiếu cho đề nghị khác")

// Vote kiểm đề nghị d theo luật trên; được thì ghi phiếu vào v (người gọi lưu v rồi mới ký).
func (s *State) Vote(v *Votes, d *Decision, proof []*Decision) error {
	if err := s.Check(d); err != nil {
		return err
	}
	hash, err := d.Hash()
	if err != nil {
		return err
	}
	if v.Seq != d.Seq {
		*v = Votes{Seq: d.Seq}
	}
	if v.ByRound == nil {
		v.ByRound = map[uint32]string{}
	}
	if h, ok := v.ByRound[d.Round]; ok {
		if h == hash {
			return nil
		}
		return fmt.Errorf("%w (seq %d, round %d)", ErrVoted, d.Seq, d.Round)
	}
	if d.Round < v.MaxRound {
		return fmt.Errorf("%w: đã sang round %d", ErrVoted, v.MaxRound)
	}
	if d.Round > 1 {
		if err := s.ValidAbort(d.Seq, d.Round-1, proof); err != nil {
			return err
		}
	}
	v.ByRound[d.Round] = hash
	if d.Round > v.MaxRound {
		v.MaxRound = d.Round
	}
	return nil
}

// ValidAbort: proof (các đề nghị của (seq, round), kèm chữ ký) có chứng minh round đó không
// đề nghị nào đủ chữ ký được không.
func (s *State) ValidAbort(seq uint64, round uint32, proof []*Decision) error {
	valid := s.validKeys()
	n := len(valid)
	voted := map[string]bool{}
	type cand struct {
		need   int
		voters map[string]bool
	}
	cands := map[string]*cand{}
	for _, d := range proof {
		if d == nil || d.Seq != seq || d.Round != round {
			return errors.New("bằng chứng có đề nghị không cùng seq / round")
		}
		r, err := s.check(d)
		if err != nil {
			return fmt.Errorf("bằng chứng có đề nghị không hợp lệ: %w", err)
		}
		h, _ := d.Hash()
		c := cands[h]
		if c == nil {
			c = &cand{need: r.need, voters: map[string]bool{}}
			cands[h] = c
		}
		for id := range d.Sigs {
			if _, ok := valid[id]; ok && s.VerifySig(d, id) {
				c.voters[id] = true
				voted[id] = true
			}
		}
	}
	if len(voted) == 0 {
		return errors.New("thiếu bằng chứng round trước")
	}
	free := n - len(voted)
	for _, c := range cands {
		if len(c.voters)+free >= c.need {
			return errors.New("bằng chứng chưa cho thấy round trước không ai đủ phiếu")
		}
	}
	return nil
}

// Equivocators: các node hợp lệ đã ký cả a và b dù a, b là hai đề nghị khác nhau cùng (seq, round).
func (s *State) Equivocators(a, b *Decision) []string {
	if a == nil || b == nil || a.Seq != b.Seq || a.Round != b.Round {
		return nil
	}
	ha, _ := a.Hash()
	hb, _ := b.Hash()
	if ha == hb {
		return nil
	}
	var out []string
	for id := range a.Sigs {
		if _, ok := b.Sigs[id]; ok && s.VerifySig(a, id) && s.VerifySig(b, id) {
			out = append(out, id)
		}
	}
	return out
}
