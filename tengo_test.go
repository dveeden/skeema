package tengo

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	UseFilteredDriverLogger()
	os.Exit(m.Run())
}

func TestIntegration(t *testing.T) {
	images := SplitEnv("TENGO_TEST_IMAGES")
	if len(images) == 0 {
		fmt.Println("TENGO_TEST_IMAGES env var is not set, so integration tests will be skipped!")
		fmt.Println("To run integration tests, you may set TENGO_TEST_IMAGES to a comma-separated")
		fmt.Println("list of Docker images. Example:\n# TENGO_TEST_IMAGES=\"mysql:5.6,mysql:5.7\" go test")
	}
	RunSuite(&TengoIntegrationSuite{}, t, images)
}

type TengoIntegrationSuite struct {
	d *DockerizedInstance
}

func (s *TengoIntegrationSuite) Setup(backend string) (err error) {
	s.d, err = CreateDockerizedInstance(backend)
	return err
}

func (s *TengoIntegrationSuite) Teardown(backend string) error {
	return s.d.Destroy()
}

func (s *TengoIntegrationSuite) BeforeTest(method string, backend string) error {
	if err := s.d.NukeData(); err != nil {
		return err
	}
	_, err := s.d.SourceSQL("testdata/integration.sql")
	return err
}

func (s *TengoIntegrationSuite) GetSchema(t *testing.T, schemaName string) *Schema {
	t.Helper()
	schema, err := s.d.Schema(schemaName)
	if schema == nil || err != nil {
		t.Fatalf("Unable to obtain schema %s: %s", schemaName, err)
	}
	return schema
}

func (s *TengoIntegrationSuite) GetTable(t *testing.T, schemaName, tableName string) *Table {
	t.Helper()
	_, table := s.GetSchemaAndTable(t, schemaName, tableName)
	return table
}

func (s *TengoIntegrationSuite) GetSchemaAndTable(t *testing.T, schemaName, tableName string) (*Schema, *Table) {
	t.Helper()
	schema := s.GetSchema(t, schemaName)
	table := schema.Table(tableName)
	if table == nil {
		t.Fatalf("Table %s.%s unexpectedly does not exist", schemaName, tableName)
	}
	return schema, table
}

func primaryKey(cols ...*Column) *Index {
	return &Index{
		Name:       "PRIMARY",
		Columns:    cols,
		SubParts:   make([]uint16, len(cols)),
		PrimaryKey: true,
		Unique:     true,
	}
}

func aTable(nextAutoInc uint64) Table {
	columns := []*Column{
		{
			Name:          "actor_id",
			TypeInDB:      "smallint(5) unsigned",
			AutoIncrement: true,
			Default:       ColumnDefaultNull,
		},
		{
			Name:     "first_name",
			TypeInDB: "varchar(45)",
			Default:  ColumnDefaultNull,
		},
		{
			Name:     "last_name",
			Nullable: true,
			TypeInDB: "varchar(45)",
			Default:  ColumnDefaultNull,
		},
		{
			Name:     "last_update",
			TypeInDB: "timestamp(2)",
			Default:  ColumnDefaultExpression("CURRENT_TIMESTAMP(2)"),
			OnUpdate: "CURRENT_TIMESTAMP(2)",
		},
		{
			Name:     "ssn",
			TypeInDB: "char(10)",
			Default:  ColumnDefaultNull,
		},
		{
			Name:     "alive",
			TypeInDB: "tinyint(1)",
			Default:  ColumnDefaultValue("1"),
		},
		{
			Name:     "alive_bit",
			TypeInDB: "bit(1)",
			Default:  ColumnDefaultExpression("b'1'"),
		},
	}
	secondaryIndexes := []*Index{
		{
			Name:     "idx_ssn",
			Columns:  []*Column{columns[4]},
			SubParts: []uint16{0},
			Unique:   true,
		},
		{
			Name:     "idx_actor_name",
			Columns:  []*Column{columns[2], columns[1]},
			SubParts: []uint16{10, 1},
		},
	}

	var autoIncClause string
	if nextAutoInc > 1 {
		autoIncClause = fmt.Sprintf(" AUTO_INCREMENT=%d", nextAutoInc)
	}
	stmt := fmt.Sprintf(`CREATE TABLE `+"`"+`actor`+"`"+` (
  `+"`"+`actor_id`+"`"+` smallint(5) unsigned NOT NULL AUTO_INCREMENT,
  `+"`"+`first_name`+"`"+` varchar(45) NOT NULL,
  `+"`"+`last_name`+"`"+` varchar(45) DEFAULT NULL,
  `+"`"+`last_update`+"`"+` timestamp(2) NOT NULL DEFAULT CURRENT_TIMESTAMP(2) ON UPDATE CURRENT_TIMESTAMP(2),
  `+"`"+`ssn`+"`"+` char(10) NOT NULL,
  `+"`"+`alive`+"`"+` tinyint(1) NOT NULL DEFAULT '1',
  `+"`"+`alive_bit`+"`"+` bit(1) NOT NULL DEFAULT b'1',
  PRIMARY KEY (`+"`"+`actor_id`+"`"+`),
  UNIQUE KEY `+"`"+`idx_ssn`+"`"+` (`+"`"+`ssn`+"`"+`),
  KEY `+"`"+`idx_actor_name`+"`"+` (`+"`"+`last_name`+"`"+`(10),`+"`"+`first_name`+"`"+`(1))
) ENGINE=InnoDB%s DEFAULT CHARSET=utf8`, autoIncClause)
	return Table{
		Name:              "actor",
		Engine:            "InnoDB",
		CharSet:           "utf8",
		Columns:           columns,
		PrimaryKey:        primaryKey(columns[0]),
		SecondaryIndexes:  secondaryIndexes,
		NextAutoIncrement: nextAutoInc,
		CreateStatement:   stmt,
	}
}

func anotherTable() Table {
	columns := []*Column{
		{
			Name:     "actor_id",
			TypeInDB: "smallint(5) unsigned",
			Default:  ColumnDefaultNull,
		},
		{
			Name:     "film_name",
			TypeInDB: "varchar(60)",
			Default:  ColumnDefaultNull,
		},
	}
	secondaryIndex := &Index{
		Name:     "film_name",
		Columns:  []*Column{columns[1]},
		SubParts: []uint16{0},
	}
	stmt := `CREATE TABLE ` + "`" + `actor_in_film` + "`" + ` (
  ` + "`" + `actor_id` + "`" + ` smallint(5) unsigned NOT NULL,
  ` + "`" + `film_name` + "`" + ` varchar(60) NOT NULL,
  PRIMARY KEY (` + "`" + `actor_id` + "`" + `,` + "`" + `film_name` + "`" + `),
  KEY ` + "`" + `film_name` + "`" + ` (` + "`" + `film_name` + "`" + `)
) ENGINE=InnoDB DEFAULT CHARSET=latin1`
	return Table{
		Name:             "actor_in_film",
		Engine:           "InnoDB",
		CharSet:          "latin1",
		Columns:          columns,
		PrimaryKey:       primaryKey(columns[0], columns[1]),
		SecondaryIndexes: []*Index{secondaryIndex},
		CreateStatement:  stmt,
	}
}

func unsupportedTable() Table {
	t := supportedTable()
	t.CreateStatement += ` ROW_FORMAT=REDUNDANT
   /*!50100 PARTITION BY RANGE (customer_id)
   (PARTITION p0 VALUES LESS THAN (123) ENGINE = InnoDB,
    PARTITION p1 VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */`
	t.UnsupportedDDL = true
	return t
}

// Returns the same as unsupportedTable() but without partitioning, so that
// the table is actually supported.
func supportedTable() Table {
	columns := []*Column{
		{
			Name:          "id",
			TypeInDB:      "int(10) unsigned",
			AutoIncrement: true,
			Default:       ColumnDefaultNull,
		},
		{
			Name:     "customer_id",
			TypeInDB: "int(10) unsigned",
			Default:  ColumnDefaultNull,
		},
		{
			Name:     "info",
			Nullable: true,
			TypeInDB: "text",
			Default:  ColumnDefaultNull,
		},
	}
	stmt := strings.Replace(`CREATE TABLE ~orders~ (
  ~id~ int(10) unsigned NOT NULL AUTO_INCREMENT,
  ~customer_id~ int(10) unsigned NOT NULL,
  ~info~ text,
  PRIMARY KEY (~id~,~customer_id~)
) ENGINE=InnoDB DEFAULT CHARSET=latin1`, "~", "`", -1)
	return Table{
		Name:              "orders",
		Engine:            "InnoDB",
		CharSet:           "latin1",
		Columns:           columns,
		PrimaryKey:        primaryKey(columns[0:2]...),
		SecondaryIndexes:  []*Index{},
		NextAutoIncrement: 1,
		CreateStatement:   stmt,
	}
}

func aSchema(name string, tables ...*Table) Schema {
	if tables == nil {
		tables = []*Table{}
	}
	s := Schema{
		Name:      name,
		CharSet:   "latin1",
		Collation: "latin1_swedish_ci",
		Tables:    tables,
	}
	return s
}

// aFkTestTable - Generates the test table for testing foreign key constraints
func aFkTestTable(nextAutoInc uint64) Table {
	// fkATable is meant to reference fkBTable when used in the test
	columns := []*Column{
		&Column{
			Name:          "id",
			TypeInDB:      "int(11) unsigned NOT NULL AUTO_INCREMENT,",
			AutoIncrement: true,
			Default:       ColumnDefaultNull,
		},
		&Column{
			Name:     "bID",
			TypeInDB: "int(11) unsigned DEFAULT NULL",
			Default:  ColumnDefaultNull,
		},
		&Column{
			Name:     "cID",
			TypeInDB: "int(11) unsigned DEFAULT NULL",
			Default:  ColumnDefaultNull,
		},
	}

	secondaryIndex := &Index{
		Name:     "cID",
		Columns:  []*Column{columns[2]},
		SubParts: []uint16{0},
	}

	foreignKey := &ForeignKey{
		Name:                 "fkatable_ibfk_2",
		Column:               columns[2],
		ReferencedSchemaName: "", // LEAVE BLANK TO SIGNAL ITS THE SAME SCHEMA AS THE CURRENT TABLE
		ReferencedTableName:  "fkCTable",
		ReferencedColumnName: "id",
		DeleteRule:           "SET NULL",
		UpdateRule:           "CASCADE",
	}

	var autoIncClause string
	if nextAutoInc > 1 {
		autoIncClause = fmt.Sprintf(" AUTO_INCREMENT=%d", nextAutoInc)
	}
	stmt := fmt.Sprintf(
		"CREATE TABLE `fkATable` ("+
			"`id` int(11) unsigned NOT NULL AUTO_INCREMENT,"+
			"`bID` int(11) unsigned DEFAULT NULL,"+
			"`cID` int(11) unsigned DEFAULT NULL,"+
			"PRIMARY KEY (`id`),"+
			"KEY `cID` (`cID`),"+
			"CONSTRAINT `fkatable_ibfk_2` FOREIGN KEY (`cID`) REFERENCES `fkCTable` (`id`) ON DELETE SET NULL ON UPDATE CASCADE"+
			") ENGINE=InnoDB%s DEFAULT CHARSET=utf8;", autoIncClause)

	return Table{
		Name:              "fkATable",
		Engine:            "InnoDB",
		CharSet:           "utf8",
		Columns:           columns,
		PrimaryKey:        primaryKey(columns[0]),
		SecondaryIndexes:  []*Index{secondaryIndex},
		ForeignKeys:       []*ForeignKey{foreignKey},
		NextAutoIncrement: nextAutoInc,
		CreateStatement:   stmt,
	}
}
