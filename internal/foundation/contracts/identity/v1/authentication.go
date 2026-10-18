package identity

import "context"

type TokenView struct {
	Policy Policy
	ID     uint
	UserID uint
	Name   string
	Models []string
	// RPMLimit/TPMLimit \u662f\u9650\u6d41\u9608\u503c\uff0c0 \u8868\u793a\u4e0d\u9650\u3002
	// \u9650\u6d41\u4e2d\u95f4\u4ef6\u6302\u5728 Auth \u4e4b\u540e\u3001\u53ea\u770b\u4e0a\u4e0b\u6587\uff0c\u6240\u4ee5\u9608\u503c\u5fc5\u987b\u968f\u6295\u5f71\u4e00\u8d77
	// \u5e26\u8fc7\u6765\u2014\u2014\u8ba9\u5b83\u81ea\u5df1\u56de\u5e93\u67e5\u4f1a\u628a\u4e00\u6b21\u8bf7\u6c42\u53d8\u6210\u4e24\u6b21\u67e5\u5e93\u3002
	RPMLimit             int
	TPMLimit             int
	UserRPMLimit         int
	UserConcurrencyLimit int
	KeyConcurrencyLimit  int
}

func (v TokenView) Principal() Principal {
	return Principal{
		UserID: v.UserID, TokenID: v.ID, TokenName: v.Name,
		Policy:   Policy{Mode: v.Policy.Mode, Models: append([]string(nil), v.Policy.Models...)},
		RPMLimit: v.RPMLimit, TPMLimit: v.TPMLimit, UserRPMLimit: v.UserRPMLimit,
		UserConcurrencyLimit: v.UserConcurrencyLimit, KeyConcurrencyLimit: v.KeyConcurrencyLimit,
	}
}

type Authenticator interface {
	Authenticate(context.Context, string) (context.Context, TokenView, bool)
}
