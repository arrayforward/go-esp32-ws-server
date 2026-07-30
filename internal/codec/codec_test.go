package codec

import (
	"math"
	"testing"
)

func TestRegistry(t *testing.T) {
	for _, id := range []int{IDPCM16, IDG711A, IDG711U, IDIMADPCM, IDOpus} {
		c, err := New(id)
		if err != nil {
			t.Fatalf("codec %d: %v", id, err)
		}
		if c.Name() == "" {
			t.Fatalf("codec %d: empty name", id)
		}
		c.Close()
	}
	if _, err := New(99); err == nil {
		t.Fatal("expected error for unknown codec")
	}
}

func TestPCM16Roundtrip(t *testing.T) {
	c, _ := New(IDPCM16)
	defer c.Close()
	in := []int16{0, 1, -1, 1000, -1000, 32767, -32768, 42}
	enc, err := c.Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(enc) != 16 {
		t.Fatalf("len=%d", len(enc))
	}
	dec, err := c.Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	for i := range in {
		if in[i] != dec[i] {
			t.Fatalf("sample %d: %d != %d", i, in[i], dec[i])
		}
	}
}

func TestG711ASilenceAndRoundtrip(t *testing.T) {
	c, _ := New(IDG711A)
	defer c.Close()
	enc, err := c.Encode([]int16{0, 0, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range enc {
		if b != 0xD5 {
			t.Fatalf("silence[%d]=0x%02x, want 0xD5", i, b)
		}
	}
	dec, err := c.Decode([]byte{0xD5})
	if err != nil {
		t.Fatal(err)
	}
	if dec[0] != 8 {
		t.Fatalf("decode(0xD5)=%d, want 8", dec[0])
	}

	in := []int16{100, -100, 1000, -1000, 8000, -8000, 30000, -30000}
	enc, _ = c.Encode(in)
	dec, _ = c.Decode(enc)
	for i := range in {
		if in[i] > 100 && dec[i] <= 0 {
			t.Fatalf("sign lost at %d: %d -> %d", i, in[i], dec[i])
		}
		if in[i] < -100 && dec[i] >= 0 {
			t.Fatalf("sign lost at %d: %d -> %d", i, in[i], dec[i])
		}
		tol := int32(40) + int32(abs16(in[i]))/8
		if d := int32(dec[i]) - int32(in[i]); d > tol || d < -tol {
			t.Fatalf("sample %d error %d > %d", i, d, tol)
		}
	}
}

func TestG711USilenceAndRoundtrip(t *testing.T) {
	c, _ := New(IDG711U)
	defer c.Close()
	enc, err := c.Encode([]int16{0, 0})
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range enc {
		if b != 0xFF {
			t.Fatalf("silence[%d]=0x%02x, want 0xFF", i, b)
		}
	}
	in := []int16{100, -100, 1000, -1000, 8000, -8000, 30000, -30000}
	enc, _ = c.Encode(in)
	dec, _ := c.Decode(enc)
	for i := range in {
		if in[i] > 0 && dec[i] <= 0 {
			t.Fatalf("sign lost at %d", i)
		}
		if in[i] < 0 && dec[i] >= 0 {
			t.Fatalf("sign lost at %d", i)
		}
		tol := int32(200) + int32(abs16(in[i]))/8
		if d := int32(dec[i]) - int32(in[i]); d > tol || d < -tol {
			t.Fatalf("sample %d error %d > %d", i, d, tol)
		}
	}
}

func TestADPCMRatioAndRoundtrip(t *testing.T) {
	c, _ := New(IDIMADPCM)
	defer c.Close()
	in := make([]int16, 160)
	enc, err := c.Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(enc) != 80 {
		t.Fatalf("ratio: 160 samples -> %d bytes, want 80", len(enc))
	}

	for i := range in {
		in[i] = int16(8000 * math.Sin(float64(i)*0.1))
	}
	enc, _ = c.Encode(in)
	dec, err := c.Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(dec) != 160 {
		t.Fatalf("decoded %d samples, want 160", len(dec))
	}
	for i := 16; i < 160; i++ {
		if d := int32(dec[i]) - int32(in[i]); d > 3000 || d < -3000 {
			t.Fatalf("sample %d error %d", i, d)
		}
	}
}

func TestOpusRoundtrip(t *testing.T) {
	c, _ := New(IDOpus)
	defer c.Close()
	const frame = 320 // 20ms @16k
	in := make([]int16, frame)
	for i := range in {
		in[i] = int16(8000 * math.Sin(2*math.Pi*1000*float64(i)/16000))
	}
	var eIn, eDec float64
	for f := 0; f < 10; f++ {
		enc, err := c.Encode(in)
		if err != nil {
			t.Fatal(err)
		}
		if len(enc) == 0 || len(enc) > 256 {
			t.Fatalf("packet %d bytes", len(enc))
		}
		dec, err := c.Decode(enc)
		if err != nil {
			t.Fatal(err)
		}
		if len(dec) != frame {
			t.Fatalf("decoded %d samples, want %d", len(dec), frame)
		}
		if f >= 5 {
			for i := range in {
				eIn += float64(in[i]) * float64(in[i])
				eDec += float64(dec[i]) * float64(dec[i])
			}
		}
	}
	t.Logf("energy in=%.0f dec=%.0f", eIn, eDec)
	if eDec < eIn*0.5 || eDec > eIn*1.5 {
		t.Fatalf("energy ratio off: in=%.0f dec=%.0f", eIn, eDec)
	}
}

func TestResample(t *testing.T) {
	up := ResampleTo16k([]int16{0, 1000, -1000}, 8000)
	if len(up) != 6 {
		t.Fatalf("up len=%d", len(up))
	}
	if up[0] != 0 || up[2] != 1000 || up[4] != -1000 {
		t.Fatalf("up samples wrong: %v", up)
	}
	down := ResampleFrom16k([]int16{100, 300, -100, -300}, 8000)
	if len(down) != 2 || down[0] != 200 || down[1] != -200 {
		t.Fatalf("down samples wrong: %v", down)
	}
}

func abs16(v int16) int {
	if v < 0 {
		return -int(v)
	}
	return int(v)
}
