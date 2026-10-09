package model

import "gorm.io/gorm"

// RegistrationCodeRecord is an administration ledger. Redis remains the
// authority for code validity, single use and compensation deadlines.
type RegistrationCodeRecord struct {
	Id          int    `json:"id"`
	Name        string `json:"name" gorm:"type:varchar(64);index"`
	Digest      string `json:"-" gorm:"type:char(64);uniqueIndex"`
	Ciphertext  string `json:"-" gorm:"type:text"`
	CreatedTime int64  `json:"created_time" gorm:"type:bigint"`
	ExpiredTime int64  `json:"expired_time" gorm:"type:bigint"`
	UsedTime    int64  `json:"used_time" gorm:"type:bigint"`
	UseRef      string `json:"-" gorm:"type:varchar(64)"`
	CreatedBy   int    `json:"created_by"`
}

func SaveRegistrationCodeRecords(records []RegistrationCodeRecord) error {
	return DB.Transaction(func(tx *gorm.DB) error { return tx.Create(&records).Error })
}

func GetRegistrationCodeRecords(page, size int, keyword string) ([]RegistrationCodeRecord, int64, error) {
	query := DB.Model(&RegistrationCodeRecord{})
	if keyword != "" {
		query = query.Where("name LIKE ?", "%"+keyword+"%")
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	records := make([]RegistrationCodeRecord, 0)
	err := query.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&records).Error
	return records, total, err
}

func RecordRegistrationCodeConsumption(digest string, timestamp int64, useRef string) error {
	// Redis-only service fixtures and pre-ledger callers have no database.
	if DB == nil {
		return nil
	}
	return DB.Model(&RegistrationCodeRecord{}).Where("digest = ?", digest).Updates(map[string]any{"used_time": timestamp, "use_ref": useRef}).Error
}

func ResetRegistrationCodeConsumption(digest, useRef string) error {
	if DB == nil {
		return nil
	}
	return DB.Model(&RegistrationCodeRecord{}).Where("digest = ? AND use_ref = ?", digest, useRef).Updates(map[string]any{"used_time": 0, "use_ref": ""}).Error
}
