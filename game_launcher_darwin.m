#import <AppKit/AppKit.h>
#import <UniformTypeIdentifiers/UniformTypeIdentifiers.h>
#import <dispatch/dispatch.h>
#import <stdlib.h>
#import <string.h>

#import "game_launcher_darwin.h"

static char *idle_lineage_copy_utf8(NSString *value) {
    if (value == nil) {
        return NULL;
    }
    const char *utf8 = [value UTF8String];
    return utf8 == NULL ? NULL : strdup(utf8);
}

static NSString *idle_lineage_canonical_application_path(NSURL *applicationURL) {
    if (applicationURL == nil || ![applicationURL isFileURL]) {
        return nil;
    }
    return [[[applicationURL URLByStandardizingPath] URLByResolvingSymlinksInPath] path];
}

static NSSet<NSString *> *idle_lineage_application_paths(NSArray<NSURL *> *applicationURLs) {
    NSMutableSet<NSString *> *paths = [NSMutableSet setWithCapacity:[applicationURLs count]];
    for (NSURL *applicationURL in applicationURLs) {
        NSString *path = idle_lineage_canonical_application_path(applicationURL);
        if (path != nil) {
            [paths addObject:path];
        }
    }
    return paths;
}

static NSString *idle_lineage_handler_rank_for_scheme(NSBundle *bundle, NSString *targetScheme) {
    id rawURLTypes = [[bundle infoDictionary] objectForKey:@"CFBundleURLTypes"];
    if (![rawURLTypes isKindOfClass:[NSArray class]]) {
        return @"";
    }

    BOOL foundScheme = NO;
    BOOL foundOwner = NO;
    BOOL foundDefault = NO;
    BOOL foundUnknown = NO;
    BOOL foundAlternate = NO;
    BOOL foundNone = NO;

    for (id rawURLType in (NSArray *)rawURLTypes) {
        if (![rawURLType isKindOfClass:[NSDictionary class]]) {
            continue;
        }

        NSDictionary *urlType = (NSDictionary *)rawURLType;
        id rawSchemes = [urlType objectForKey:@"CFBundleURLSchemes"];
        NSArray *schemes = nil;
        if ([rawSchemes isKindOfClass:[NSArray class]]) {
            schemes = (NSArray *)rawSchemes;
        } else if ([rawSchemes isKindOfClass:[NSString class]]) {
            schemes = @[rawSchemes];
        } else {
            continue;
        }

        BOOL matchesScheme = NO;
        for (id rawScheme in schemes) {
            if ([rawScheme isKindOfClass:[NSString class]] &&
                [(NSString *)rawScheme caseInsensitiveCompare:targetScheme] == NSOrderedSame) {
                matchesScheme = YES;
                break;
            }
        }
        if (!matchesScheme) {
            continue;
        }

        foundScheme = YES;
        id rawRank = [urlType objectForKey:@"LSHandlerRank"];
        if (rawRank == nil) {
            // Launch Services treats an omitted rank as Default.
            foundDefault = YES;
            continue;
        }
        if (![rawRank isKindOfClass:[NSString class]]) {
            foundUnknown = YES;
            continue;
        }

        NSString *rank = [(NSString *)rawRank stringByTrimmingCharactersInSet:[NSCharacterSet whitespaceAndNewlineCharacterSet]];
        if ([rank length] == 0 || [rank caseInsensitiveCompare:@"Default"] == NSOrderedSame) {
            foundDefault = YES;
        } else if ([rank caseInsensitiveCompare:@"Owner"] == NSOrderedSame) {
            foundOwner = YES;
        } else if ([rank caseInsensitiveCompare:@"Alternate"] == NSOrderedSame) {
            foundAlternate = YES;
        } else if ([rank caseInsensitiveCompare:@"None"] == NSOrderedSame) {
            foundNone = YES;
        } else {
            foundUnknown = YES;
        }
    }

    if (!foundScheme) {
        // NSWorkspace may know about dynamically registered handlers which have
        // no matching static plist declaration. Preserve those candidates.
        return @"";
    }
    if (foundOwner) {
        return @"Owner";
    }
    if (foundDefault) {
        return @"Default";
    }
    if (foundUnknown) {
        return @"Unknown";
    }
    if (foundAlternate) {
        return @"Alternate";
    }
    if (foundNone) {
        return @"None";
    }
    return @"";
}

char *idle_lineage_copy_game_browsers_json(char **error_message) {
    @autoreleasepool {
        if (error_message != NULL) {
            *error_message = NULL;
        }

        NSWorkspace *workspace = [NSWorkspace sharedWorkspace];
        NSArray<NSURL *> *httpApplications = [workspace URLsForApplicationsToOpenURL:[NSURL URLWithString:@"http://example.invalid/"]];
        NSArray<NSURL *> *httpsApplications = [workspace URLsForApplicationsToOpenURL:[NSURL URLWithString:@"https://example.invalid/"]];
        NSArray<NSURL *> *htmlApplications = [workspace URLsForApplicationsToOpenContentType:UTTypeHTML];
        NSSet<NSString *> *httpPaths = idle_lineage_application_paths(httpApplications);
        NSSet<NSString *> *httpsPaths = idle_lineage_application_paths(httpsApplications);
        NSMutableArray<NSDictionary<NSString *, NSString *> *> *records = [NSMutableArray array];
        NSMutableSet<NSString *> *seenPaths = [NSMutableSet set];

        for (NSURL *applicationURL in htmlApplications) {
            NSString *path = idle_lineage_canonical_application_path(applicationURL);
            if (path == nil || [seenPaths containsObject:path] || ![httpPaths containsObject:path] || ![httpsPaths containsObject:path]) {
                continue;
            }
            [seenPaths addObject:path];

            NSBundle *bundle = [NSBundle bundleWithURL:applicationURL];
            NSString *bundleID = [bundle bundleIdentifier] ?: @"";
            NSString *name = [bundle objectForInfoDictionaryKey:@"CFBundleDisplayName"];
            if ([name length] == 0) {
                name = [bundle objectForInfoDictionaryKey:@"CFBundleName"];
            }
            if ([name length] == 0) {
                name = [[NSFileManager defaultManager] displayNameAtPath:path];
            }
            if ([name length] == 0) {
                name = [[path lastPathComponent] stringByDeletingPathExtension];
            }

            NSString *httpHandlerRank = idle_lineage_handler_rank_for_scheme(bundle, @"http");
            NSString *httpsHandlerRank = idle_lineage_handler_rank_for_scheme(bundle, @"https");

            [records addObject:@{
                @"name": name ?: @"Browser",
                @"bundleID": bundleID,
                @"applicationPath": path,
                @"httpHandlerRank": httpHandlerRank,
                @"httpsHandlerRank": httpsHandlerRank,
            }];
        }

        NSError *serializationError = nil;
        NSData *json = [NSJSONSerialization dataWithJSONObject:records options:0 error:&serializationError];
        if (json == nil) {
            if (error_message != NULL) {
                *error_message = idle_lineage_copy_utf8([serializationError localizedDescription] ?: @"Unable to serialize browser list");
            }
            return NULL;
        }

        NSString *jsonString = [[[NSString alloc] initWithData:json encoding:NSUTF8StringEncoding] autorelease];
        return idle_lineage_copy_utf8(jsonString);
    }
}

static int idle_lineage_open_game(const char *file_path, const char *application_path, char **error_message) {
    @autoreleasepool {
        if (error_message != NULL) {
            *error_message = NULL;
        }
        if (file_path == NULL) {
            if (error_message != NULL) {
                *error_message = strdup("Game entry path is missing");
            }
            return 0;
        }

        NSString *filePath = [NSString stringWithUTF8String:file_path];
        if ([filePath length] == 0) {
            if (error_message != NULL) {
                *error_message = strdup("Game entry path is invalid UTF-8");
            }
            return 0;
        }
        NSURL *fileURL = [NSURL fileURLWithPath:filePath isDirectory:NO];

        NSURL *applicationURL = nil;
        if (application_path != NULL) {
            NSString *applicationPath = [NSString stringWithUTF8String:application_path];
            if ([applicationPath length] == 0) {
                if (error_message != NULL) {
                    *error_message = strdup("Browser application path is invalid UTF-8");
                }
                return 0;
            }
            applicationURL = [NSURL fileURLWithPath:applicationPath isDirectory:YES];
        }

        dispatch_semaphore_t semaphore = dispatch_semaphore_create(0);
        __block int succeeded = 0;
        __block char *completionError = NULL;
        void (^completion)(NSRunningApplication *, NSError *) = ^(NSRunningApplication *application, NSError *error) {
            if (error == nil && application != nil) {
                succeeded = 1;
            } else {
                NSString *message = [error localizedDescription] ?: @"The browser did not accept the game file";
                completionError = idle_lineage_copy_utf8(message);
            }
            dispatch_semaphore_signal(semaphore);
        };

        NSWorkspaceOpenConfiguration *configuration = [NSWorkspaceOpenConfiguration configuration];
        if (applicationURL == nil) {
            [[NSWorkspace sharedWorkspace] openURL:fileURL configuration:configuration completionHandler:completion];
        } else {
            [[NSWorkspace sharedWorkspace] openURLs:@[fileURL]
                               withApplicationAtURL:applicationURL
                                      configuration:configuration
                                  completionHandler:completion];
        }
        dispatch_semaphore_wait(semaphore, DISPATCH_TIME_FOREVER);

        if (!succeeded && error_message != NULL) {
            *error_message = completionError != NULL ? completionError : strdup("Unable to open the game file");
            completionError = NULL;
        }
        if (completionError != NULL) {
            free(completionError);
        }
        return succeeded;
    }
}

int idle_lineage_open_game_default(const char *file_path, char **error_message) {
    return idle_lineage_open_game(file_path, NULL, error_message);
}

int idle_lineage_open_game_with_application(const char *file_path, const char *application_path, char **error_message) {
    return idle_lineage_open_game(file_path, application_path, error_message);
}
