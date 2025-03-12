package v1

type GeneralModel struct {
	Name     string    `json:"name"`
	Versions []Version `json:"versions"`
}

type Version struct {
	Name         string `json:"name"`
	CreationTime string `json:"creation_time"`
}
