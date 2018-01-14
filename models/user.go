package models

// User represents the user model for gophish.
type User struct {
	Id       int64  `json:"id"`
	Username string `json:"username" sql:"not null;unique"`
	Hash     string `json:"-"`
	ApiKey   string `json:"api_key" sql:"not null;unique"`
}

// GetUser returns the user that the given id corresponds to. If no user is found, an
// error is thrown.
func GetUser(id int64) (User, error) {
	u := User{}
	err := db.Where("id=?", id).First(&u).Error
	return u, err
}

// GetUserByAPIKey returns the user that the given API Key corresponds to. If no user is found, an
// error is thrown.
func GetUserByAPIKey(key string) (User, error) {
	u := User{}
	err := db.Where("api_key = ?", key).First(&u).Error
	return u, err
}

// GetUserByUsername returns the user that the given username corresponds to. If no user is found, an
// error is thrown.
func GetUserByUsername(username string) (User, error) {
	u := User{}
	err := db.Where("username = ?", username).First(&u).Error
	return u, err
}

// PutUser updates the given user
func PutUser(u *User) error {
	err := db.Save(u).Error
	return err
}

// DeleteUser deletes the given user
func DeleteUser(id int64) error {
	err := DeleteTemplatesByUserId(id)
	if err != nil {
		return err
	}

	err = DeleteSMTPsByUserId(id)
	if err != nil {
		return err
	}

	err = DeletePagesByUserId(id)
	if err != nil {
		return err
	}

	err = DeleteGroupsByUserId(id)
	if err != nil {
		return err
	}

	err = DeleteCampaignsByUserId(id)
	if err != nil {
		return err
	}

	var user User
	if err = db.First(&user, id).Error; err != nil {
		return err
	}

	if err = db.Delete(&user).Error; err != nil {
		return err
	}

	return nil
}
