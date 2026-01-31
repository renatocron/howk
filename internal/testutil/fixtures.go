package testutil

var (
    ValidStatusCodes = []int{200, 201, 202, 204}
    RetryableStatusCodes = []int{408, 429, 500, 502, 503, 504}
    NonRetryableStatusCodes = []int{400, 401, 403, 404, 422}
)
