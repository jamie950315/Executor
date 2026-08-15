package terminal

func flattenEnv(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	items := make([]string, 0, len(env))
	for key, value := range env {
		items = append(items, key+"="+value)
	}
	return items
}
