package butian

const (
	centerURL        = "https://www.butian.net/WhiteHat/Center"
	submitURL        = "https://www.butian.net/Loo/submit"
	projectHallURL   = "https://www.butian.net/Reward/plan/2"
	growURL          = "https://www.butian.net/Grow"
	activeCenterURL  = "https://www.butian.net/Active/center"
	publicWelfareAPI = "https://www.butian.net/Reward/pub"
	// homeURL is a public butian.net origin. The public-welfare list needs no
	// login, only a same-origin page for the observed XHR.
	homeURL   = "https://www.butian.net/"
	extractor = "butian/reader@0.1.0"
)

type pubVendor struct {
	CompanyID   string `json:"company_id"`
	CompanyName string `json:"company_name"`
	Avatar      string `json:"avatar"`
}

type publicWelfareResponse struct {
	Status  int    `json:"status"`
	Message string `json:"info"`
	Data    struct {
		Count   int         `json:"count"`
		Current int         `json:"current"`
		List    []pubVendor `json:"list"`
	} `json:"data"`
}

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
