#ifndef IDLE_LINEAGE_GAME_LAUNCHER_DARWIN_H
#define IDLE_LINEAGE_GAME_LAUNCHER_DARWIN_H

char *idle_lineage_copy_game_browsers_json(char **error_message);
int idle_lineage_open_game_default(const char *file_path, char **error_message);
int idle_lineage_open_game_with_application(const char *file_path, const char *application_path, char **error_message);

#endif
