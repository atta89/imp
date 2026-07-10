package models

// ImportRow is the parsed shape of a single CSV/XLSX row during a bulk PO
// import. NOT part of the public REST API — never serialized to JSON. It is
// stored on the import_jobs document so the commit step can re-load the
// parsed payload without re-parsing the original file.
//
// All fields are strings as read from the file; the resolver casts and
// validates them.
type ImportRow struct {
	RowNum                 int               `bson:"rowNum"`
	PONumber               string            `bson:"poNumber"`
	SupplierName           string            `bson:"supplierName"`
	SupplierContact        string            `bson:"supplierContact,omitempty"`
	OrderDate              string            `bson:"orderDate"`
	PONotes                string            `bson:"poNotes,omitempty"`
	POResponsibleUserEmail string            `bson:"poResponsibleUserEmail"`
	LineItemName           string            `bson:"lineItemName"`
	CategorySlug           string            `bson:"categorySlug"`
	Quantity               string            `bson:"quantity"`
	HomeVenueCode          string            `bson:"homeVenueCode"`
	DepartmentCode         string            `bson:"departmentCode,omitempty"`
	AssetTag               string            `bson:"assetTag,omitempty"`
	Status                 string            `bson:"status,omitempty"`
	Condition              string            `bson:"condition,omitempty"`
	CurrentVenueCode       string            `bson:"currentVenueCode,omitempty"`
	ResponsibleUserEmail   string            `bson:"responsibleUserEmail,omitempty"`
	SerialNumber           string            `bson:"serialNumber,omitempty"`
	PurchaseDate           string            `bson:"purchaseDate,omitempty"`
	ExpectedReturnDate     string            `bson:"expectedReturnDate,omitempty"`
	Notes                  string            `bson:"notes,omitempty"`
	SpecFields             map[string]string `bson:"specFields,omitempty"`
}
