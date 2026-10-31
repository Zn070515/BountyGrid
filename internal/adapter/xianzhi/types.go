package xianzhi

const (
	taskListURL     = "https://yundun.console.aliyun.com/?p=xznew#/taskmanagement/tasks/all"
	profileURL      = "https://yundun.console.aliyun.com/?p=xznew#/personal-center/personal-info"
	financeURL      = "https://yundun.console.aliyun.com/?p=xznew#/personal-center/finance-manage"
	vulnURL         = "https://yundun.console.aliyun.com/?p=xznew#/vulnerabilityManagement/whiteHat"
	detailURLFormat = "https://yundun.console.aliyun.com/?p=xznew#/taskmanagement/tasks/detail/%s"
	extractor       = "xianzhi/reader@0.1.0"
)

// taskCard is the compact task data extracted from the React fiber of one
// .card-item. Names/titles come from the component props; bounty is the card
// text's 赏金范围 value.
type taskCard struct {
	ID                int64    `json:"id"`
	Name              string   `json:"name"`
	Status            int      `json:"status"`
	Type              int      `json:"type"`
	RegistrationBegin int64    `json:"registrationBegin"`
	RegistrationEnd   int64    `json:"registrationEnd"`
	TaskBegin         int64    `json:"taskBegin"`
	TaskEnd           int64    `json:"taskEnd"`
	RegistrationLimit int      `json:"registrationLimit"`
	Bounty            string   `json:"bounty"`
	Buttons           []string `json:"buttons"`
	Title             string   `json:"title"`
	Missing           bool     `json:"missing"`
}

type taskChunk struct {
	Cards   []taskCard `json:"cards"`
	MaxPage int        `json:"maxPage"`
}
