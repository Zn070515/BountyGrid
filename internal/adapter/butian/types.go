package butian

const (
	centerURL      = "https://www.butian.net/WhiteHat/Center"
	submitURL      = "https://www.butian.net/Loo/submit"
	projectHallURL = "https://www.butian.net/Reward/plan/2"
	growURL        = "https://www.butian.net/Grow"
	extractor      = "butian/reader@0.1.0"
)

type srcCard struct {
	Name    string   `json:"name"`
	Text    string   `json:"text"`
	Buttons []string `json:"buttons"`
	Missing bool     `json:"missing"`
}

type srcChunk struct {
	Cards  []srcCard `json:"cards"`
	Total  int       `json:"total"`
	NoData bool      `json:"noData"`
}
