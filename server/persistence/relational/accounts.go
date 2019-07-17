package relational

import (
	"fmt"

	"github.com/gofrs/uuid"
	"github.com/jinzhu/gorm"
	"github.com/offen/offen/server/keys"
	"github.com/offen/offen/server/persistence"
)

func (r *relationalDatabase) GetAccount(accountID string, events bool, eventsSince string) (persistence.AccountResult, error) {
	var account Account

	queryDB := r.db
	if events {
		if eventsSince != "" {
			queryDB = queryDB.Preload("Events", "event_id > ?", eventsSince)
		} else {
			queryDB = queryDB.Preload("Events")
		}
		queryDB = queryDB.Preload("Events.User")
	}

	if err := queryDB.Find(&account, "account_id = ?", accountID).Error; err != nil {
		if gorm.IsRecordNotFoundError(err) {
			return persistence.AccountResult{}, persistence.ErrUnknownAccount(fmt.Sprintf(`relational: account id "%s" unknown`, accountID))
		}
		return persistence.AccountResult{}, fmt.Errorf("relational: error looking up account with id %s: %v", accountID, err)
	}

	key, err := account.WrapPublicKey()
	if err != nil {
		return persistence.AccountResult{}, fmt.Errorf("relational: error wrapping account public key: %v", err)
	}

	result := persistence.AccountResult{
		AccountID: account.AccountID,
	}

	if events {
		result.EncryptedSecretKey = account.EncryptedSecretKey
	} else {
		result.PublicKey = key
	}

	eventResults := persistence.EventsByAccountID{}
	userSecrets := persistence.SecretsByUserID{}

	for _, evt := range account.Events {
		eventResults[evt.AccountID] = append(eventResults[evt.AccountID], persistence.EventResult{
			UserID:    evt.HashedUserID,
			EventID:   evt.EventID,
			Payload:   evt.Payload,
			AccountID: evt.AccountID,
		})
		if evt.HashedUserID != nil {
			userSecrets[*evt.HashedUserID] = evt.User.EncryptedUserSecret
		}
	}

	if len(eventResults) != 0 {
		result.Events = &eventResults
	}
	if len(userSecrets) != 0 {
		result.UserSecrets = &userSecrets
	}

	return result, nil
}

func (r *relationalDatabase) AssociateUserSecret(accountID, userID, encryptedUserSecret string) error {
	var account Account
	if err := r.db.Find(&account, "account_id = ?", accountID).Error; err != nil {
		return fmt.Errorf("relational: error looking up account with id %s: %v", accountID, err)
	}
	hashedUserID := account.HashUserID(userID)

	var user User
	txn := r.db.Begin()
	// there is an issue with the postgres backend of GORM that disallows inserting
	// primary keys when using `FirstOrCreate`, so we need to do a manual check
	// for existence beforehand
	if err := r.db.First(&user, "hashed_user_id = ?", hashedUserID).Error; err != nil {
		if err != gorm.ErrRecordNotFound {
			return fmt.Errorf("relational: error looking up user: %v", err)
		}
	} else {
		parkedID, parkedIDErr := uuid.NewV4()
		if parkedIDErr != nil {
			txn.Rollback()
			return fmt.Errorf("relational: error migrating existing events: %v", parkedIDErr)
		}

		parkedHash := account.HashUserID(parkedID.String())
		user.HashedUserID = parkedHash

		if err := txn.Create(&user).Error; err != nil {
			txn.Rollback()
			return fmt.Errorf("relational: error migrating existing events: %v", err)
		}
		if err := txn.Delete(&User{}, "hashed_user_id = ?", hashedUserID).Error; err != nil {
			txn.Rollback()
			return fmt.Errorf("relational: error migrating existing events: %v", err)
		}

		var affected []Event
		r.db.Find(&affected, "hashed_user_id = ?", hashedUserID)

		for _, ev := range affected {
			newID, err := newEventID()
			if err != nil {
				txn.Rollback()
				return fmt.Errorf("relational: error migrating existing events: %v", err)
			}
			if err := txn.Delete(&Event{}, "event_id = ?", ev.EventID).Error; err != nil {
				txn.Rollback()
				return fmt.Errorf("relational: error migrating existing events: %v", err)
			}
			ev.EventID = newID
			ev.HashedUserID = &parkedHash
			if err := txn.Create(&ev).Error; err != nil {
				txn.Rollback()
				return fmt.Errorf("relational: error migrating existing events: %v", err)
			}
		}
	}

	if err := txn.Commit().Error; err != nil {
		return fmt.Errorf("relational: error migrating existing events: %v", err)
	}

	return r.db.Create(&User{
		EncryptedUserSecret: encryptedUserSecret,
		HashedUserID:        hashedUserID,
	}).Error
}

func (r *relationalDatabase) CreateAccount(accountID, name string) error {
	userSalt, userSaltErr := keys.GenerateRandomString(keys.UserSaltLength)
	if userSaltErr != nil {
		return fmt.Errorf("relational: error creating new user salt for account: %v", userSaltErr)
	}
	publicKey, privateKey, keyErr := keys.GenerateRSAKeypair(keys.RSAKeyLength)
	if keyErr != nil {
		return fmt.Errorf("relational: error creating new key pair for account: %v", keyErr)
	}
	encryptedPrivateKey, encryptErr := r.encryption.Encrypt(privateKey)
	if encryptErr != nil {
		return fmt.Errorf("relational: error encrypting account private key: %v", encryptErr)
	}
	return r.db.Save(&Account{
		AccountID:          accountID,
		Name:               name,
		PublicKey:          string(publicKey),
		EncryptedSecretKey: string(encryptedPrivateKey),
		UserSalt:           userSalt,
	}).Error
}
