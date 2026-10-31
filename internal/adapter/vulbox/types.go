package vulbox

const (
	apiBase         = "https://vapi.vulbox.com"
	listPageURL     = "https://www.vulbox.com/projects/list"
	userHomeURL     = "https://user.vulbox.com/home"
	profileURL      = "https://user.vulbox.com/personCenter"
	myProjectsURL   = "https://user.vulbox.com/projectHall/myProject"
	detailURLFormat = "https://user.vulbox.com/projectHall/projectDetail/%s"
	extractor       = "vulbox/reader@0.1.0"
)

// apiProject is the subset of the project list JSON the reader relies on.
// The platform returns numeric fields as strings; flexInt64 accepts both.
type apiProject struct {
	ID             flexString    `json:"id"`
	ProjectID      flexString    `json:"project_id"`
	Type           flexString    `json:"type"`
	Status         flexString    `json:"status"`
	TaskTitle      string        `json:"task_title"`
	TaskDesc       string        `json:"task_desc"`
	TaskIsSrc      flexString    `json:"task_is_src"`
	TaskWebsite    string        `json:"task_website"`
	TaskStime      flexInt64     `json:"task_stime"`
	TaskEtime      flexInt64     `json:"task_etime"`
	TaskBonusLevel flexString    `json:"task_bonus_level"`
	BonusRange     apiBonusRange `json:"bonus_range"`
	Tags           []apiTag      `json:"tags"`
	IsJoin         bool          `json:"is_join"`
	IsApply        bool          `json:"is_apply"`
	IsInvited      bool          `json:"is_invited"`
	DifficultName  string        `json:"difficult_name"`
	Goto           string        `json:"goto"`
	BusinessType   flexString    `json:"business_type"`
}

type apiBonusRange struct {
	LowMin     int64 `json:"low_min"`
	LowMax     int64 `json:"low_max"`
	MiddleMin  int64 `json:"middle_min"`
	MiddleMax  int64 `json:"middle_max"`
	HighMin    int64 `json:"high_min"`
	HighMax    int64 `json:"high_max"`
	SeriousMin int64 `json:"serious_min"`
	SeriousMax int64 `json:"serious_max"`
}

type apiTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type projectListResponse struct {
	Code    int    `json:"code"`
	Message string `json:"msg"`
	Data    struct {
		CurrentPage int          `json:"current_page"`
		LastPage    int          `json:"last_page"`
		PerPage     int          `json:"per_page"`
		Total       int          `json:"total"`
		Data        []apiProject `json:"data"`
	} `json:"data"`
}
