package storage

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/endophage/gotuf/data"
	"github.com/jinzhu/gorm"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
)

// SampleTUF returns a sample TUFFile with the given Version (ID will have
// to be set independently)
func SampleTUF(version int) TUFFile {
	return TUFFile{
		Gun:     "testGUN",
		Role:    "root",
		Version: version,
		Data:    []byte("1"),
	}
}

func SampleUpdate(version int) MetaUpdate {
	return MetaUpdate{
		Role:    "root",
		Version: version,
		Data:    []byte("1"),
	}
}

// SetUpSQLite creates a sqlite database for testing
func SetUpSQLite(dbDir string, t *testing.T) (*gorm.DB, *MySQLStorage) {
	db, err := gorm.Open("sqlite3", dbDir+"test_db")
	assert.NoError(t, err)

	// Create the DB tables
	err = CreateTUFTable(db)
	assert.NoError(t, err)

	err = CreateTimestampTable(db)
	assert.NoError(t, err)

	// verify that the tables are empty
	var count int
	for _, model := range [2]interface{}{&TUFFile{}, &TimestampKey{}} {
		query := db.Model(model).Count(&count)
		assert.NoError(t, query.Error)
		assert.Equal(t, 0, count)
	}

	return &db, NewMySQLStorage(db.DB())
}

// TestMySQLUpdateCurrent asserts that UpdateCurrent will add a new TUF file
// if no previous version existed.
func TestMySQLUpdateCurrentNew(t *testing.T) {
	tempBaseDir, err := ioutil.TempDir("", "notary-test-")
	gormDB, dbStore := SetUpSQLite(tempBaseDir, t)
	defer os.RemoveAll(tempBaseDir)

	// Adding a new TUF file should succeed
	err = dbStore.UpdateCurrent("testGUN", SampleUpdate(0))
	assert.NoError(t, err, "Creating a row in an empty DB failed.")

	// There should just be one row
	var rows []TUFFile
	query := gormDB.Select("ID, Gun, Role, Version, Data").Find(&rows)
	assert.NoError(t, query.Error)

	expected := SampleTUF(0)
	expected.ID = 1
	assert.Equal(t, []TUFFile{expected}, rows)
}

// TestMySQLUpdateCurrentNewVersion asserts that UpdateCurrent will add a
// new (higher) version of an existing TUF file
func TestMySQLUpdateCurrentNewVersion(t *testing.T) {
	tempBaseDir, err := ioutil.TempDir("", "notary-test-")
	gormDB, dbStore := SetUpSQLite(tempBaseDir, t)
	defer os.RemoveAll(tempBaseDir)

	// insert row
	oldVersion := SampleTUF(0)
	query := gormDB.Create(&oldVersion)
	assert.NoError(t, query.Error, "Creating a row in an empty DB failed.")

	// UpdateCurrent with a newer version should succeed
	update := SampleUpdate(2)
	err = dbStore.UpdateCurrent("testGUN", update)
	assert.NoError(t, err, "Creating a row in an empty DB failed.")

	// There should just be one row
	var rows []TUFFile
	query = gormDB.Select("ID, Gun, Role, Version, Data").Find(&rows)
	assert.NoError(t, query.Error)

	oldVersion.Model = gorm.Model{ID: 1}
	expected := SampleTUF(2)
	expected.Model = gorm.Model{ID: 2}
	assert.Equal(t, []TUFFile{oldVersion, expected}, rows)
}

// TestMySQLUpdateCurrentOldVersionError asserts that an error is raised if
// trying to update to an older version of a TUF file.
func TestMySQLUpdateCurrentOldVersionError(t *testing.T) {
	tempBaseDir, err := ioutil.TempDir("", "notary-test-")
	gormDB, dbStore := SetUpSQLite(tempBaseDir, t)
	defer os.RemoveAll(tempBaseDir)

	// insert row
	newVersion := SampleTUF(3)
	query := gormDB.Create(&newVersion)
	assert.NoError(t, query.Error, "Creating a row in an empty DB failed.")

	// UpdateCurrent should fail due to the version being lower than the
	// previous row
	err = dbStore.UpdateCurrent("testGUN", SampleUpdate(0))
	assert.Error(t, err, "Error should not be nil")
	assert.IsType(t, &ErrOldVersion{}, err,
		"Expected ErrOldVersion error type, got: %v", err)

	// There should just be one row
	var rows []TUFFile
	query = gormDB.Select("ID, Gun, Role, Version, Data").Find(&rows)
	assert.NoError(t, query.Error)

	newVersion.Model = gorm.Model{ID: 1}
	assert.Equal(t, []TUFFile{newVersion}, rows)

	dbStore.DB.Close()
}

// TestMySQLUpdateMany asserts that inserting multiple updates succeeds if the
// updates do not conflict with each.
func TestMySQLUpdateMany(t *testing.T) {
	tempBaseDir, err := ioutil.TempDir("", "notary-test-")
	gormDB, dbStore := SetUpSQLite(tempBaseDir, t)
	defer os.RemoveAll(tempBaseDir)

	err = dbStore.UpdateMany("testGUN", []MetaUpdate{
		SampleUpdate(0),
		{
			Role:    "targets",
			Version: 1,
			Data:    []byte("2"),
		},
		SampleUpdate(2),
	})
	assert.NoError(t, err, "UpdateMany errored unexpectedly: %v", err)

	gorm1 := SampleTUF(0)
	gorm1.ID = 1
	gorm2 := TUFFile{
		Model: gorm.Model{ID: 2}, Gun: "testGUN", Role: "targets",
		Version: 1, Data: []byte("2")}
	gorm3 := SampleTUF(2)
	gorm3.ID = 3
	expected := []TUFFile{gorm1, gorm2, gorm3}

	var rows []TUFFile
	query := gormDB.Select("ID, Gun, Role, Version, Data").Find(&rows)
	assert.NoError(t, query.Error)
	assert.Equal(t, expected, rows)

	dbStore.DB.Close()
}

// TestMySQLUpdateManyVersionOrder asserts that inserting updates with
// non-monotonic versions still succeeds.
func TestMySQLUpdateManyVersionOrder(t *testing.T) {
	tempBaseDir, err := ioutil.TempDir("", "notary-test-")
	gormDB, dbStore := SetUpSQLite(tempBaseDir, t)
	defer os.RemoveAll(tempBaseDir)

	err = dbStore.UpdateMany(
		"testGUN", []MetaUpdate{SampleUpdate(2), SampleUpdate(0)})
	assert.NoError(t, err)

	// the whole transaction should have rolled back, so there should be
	// no entries.
	gorm1 := SampleTUF(2)
	gorm1.ID = 1
	gorm2 := SampleTUF(0)
	gorm2.ID = 2

	var rows []TUFFile
	query := gormDB.Select("ID, Gun, Role, Version, Data").Find(&rows)
	assert.NoError(t, query.Error)
	assert.Equal(t, []TUFFile{gorm1, gorm2}, rows)

	dbStore.DB.Close()
}

// TestMySQLUpdateManyDuplicateRollback asserts that inserting duplicate
// updates fails.
func TestMySQLUpdateManyDuplicateRollback(t *testing.T) {
	tempBaseDir, err := ioutil.TempDir("", "notary-test-")
	gormDB, dbStore := SetUpSQLite(tempBaseDir, t)
	defer os.RemoveAll(tempBaseDir)

	update := SampleUpdate(0)
	err = dbStore.UpdateMany("testGUN", []MetaUpdate{update, update})
	assert.Error(t, err, "There should be an error updating twice.")
	// sqlite3 error and mysql error aren't compatible
	// assert.IsType(t, &ErrOldVersion{}, err,
	//               "UpdateMany returned wrong error type")

	// the whole transaction should have rolled back, so there should be
	// no entries.
	var count int
	query := gormDB.Model(&TUFFile{}).Count(&count)
	assert.NoError(t, query.Error)
	assert.Equal(t, 0, count)

	dbStore.DB.Close()
}

func TestMySQLGetCurrent(t *testing.T) {
	tempBaseDir, err := ioutil.TempDir("", "notary-test-")
	gormDB, dbStore := SetUpSQLite(tempBaseDir, t)
	defer os.RemoveAll(tempBaseDir)

	byt, err := dbStore.GetCurrent("testGUN", "root")
	assert.Nil(t, byt)
	assert.Error(t, err, "There should be an error Getting an empty table")
	assert.IsType(t, &ErrNotFound{}, err, "Should get a not found error")

	tuf := SampleTUF(0)
	query := gormDB.Create(&tuf)
	assert.NoError(t, query.Error, "Creating a row in an empty DB failed.")

	byt, err = dbStore.GetCurrent("testGUN", "root")
	assert.NoError(t, err, "There should not be any errors getting.")
	assert.Equal(t, []byte("1"), byt, "Returned data was incorrect")

	dbStore.DB.Close()
}

func TestMySQLDelete(t *testing.T) {
	tempBaseDir, err := ioutil.TempDir("", "notary-test-")
	gormDB, dbStore := SetUpSQLite(tempBaseDir, t)
	defer os.RemoveAll(tempBaseDir)

	tuf := SampleTUF(0)
	query := gormDB.Create(&tuf)
	assert.NoError(t, query.Error, "Creating a row in an empty DB failed.")

	err = dbStore.Delete("testGUN")
	assert.NoError(t, err, "There should not be any errors deleting.")

	// verify deletion
	var count int
	query = gormDB.Model(&TUFFile{}).Count(&count)
	assert.NoError(t, query.Error)
	assert.Equal(t, 0, count)

	dbStore.DB.Close()
}

func TestMySQLGetTimestampKeyNoKey(t *testing.T) {
	tempBaseDir, err := ioutil.TempDir("", "notary-test-")
	gormDB, dbStore := SetUpSQLite(tempBaseDir, t)
	defer os.RemoveAll(tempBaseDir)

	cipher, public, err := dbStore.GetTimestampKey("testGUN")
	assert.Equal(t, data.KeyAlgorithm(""), cipher)
	assert.Nil(t, public)
	assert.IsType(t, &ErrNoKey{}, err,
		"Expected ErrNoKey from GetTimestampKey")

	query := gormDB.Create(&TimestampKey{
		Gun:    "testGUN",
		Cipher: "testCipher",
		Public: []byte("1"),
	})
	assert.NoError(
		t, query.Error, "Inserting timestamp into empty DB should succeed")

	cipher, public, err = dbStore.GetTimestampKey("testGUN")
	assert.Equal(t, data.KeyAlgorithm("testCipher"), cipher,
		"Returned cipher was incorrect")
	assert.Equal(t, []byte("1"), public, "Returned pubkey was incorrect")
}

func TestMySQLSetTimestampKeyExists(t *testing.T) {
	tempBaseDir, err := ioutil.TempDir("", "notary-test-")
	gormDB, dbStore := SetUpSQLite(tempBaseDir, t)
	defer os.RemoveAll(tempBaseDir)

	err = dbStore.SetTimestampKey("testGUN", "testCipher", []byte("1"))
	assert.NoError(t, err, "Inserting timestamp into empty DB should succeed")

	err = dbStore.SetTimestampKey("testGUN", "testCipher", []byte("1"))
	assert.Error(t, err)
	// sqlite3 error and mysql error aren't compatible

	// assert.IsType(t, &ErrTimestampKeyExists{}, err,
	//               "Expected ErrTimestampKeyExists from SetTimestampKey")

	var rows []TimestampKey
	query := gormDB.Select("ID, Gun, Cipher, Public").Find(&rows)
	assert.NoError(t, query.Error)

	expected := TimestampKey{Gun: "testGUN", Cipher: "testCipher",
		Public: []byte("1")}
	expected.Model = gorm.Model{ID: 1}

	assert.Equal(t, []TimestampKey{expected}, rows)

	dbStore.DB.Close()
}
