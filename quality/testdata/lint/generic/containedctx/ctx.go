// lint:expect containedctx

package containedctx

import "context"

type Holder struct{ ctx context.Context }
