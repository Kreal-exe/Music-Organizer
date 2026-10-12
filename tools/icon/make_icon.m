// Draws the application icon the way Photo Organizer's is drawn: a blue
// rounded tile with the "music.note.list" symbol in white, rendered by AppKit
// so the symbol stays a crisp vector at every size. It needs a Mac, and is run
// by hand only when the icon changes:
//
//   clang -fobjc-arc -framework Cocoa tools/icon/make_icon.m -o "$TMPDIR/make_icon"
//   "$TMPDIR/make_icon" packaging
//
// which writes packaging/appicon.png (the macOS icon, on Apple's icon grid)
// and packaging/windows/icon.ico (full tiles, as Windows icons are, each size
// drawn on its own rather than scaled down).
#import <Cocoa/Cocoa.h>

static NSBitmapImageRep *Draw(CGFloat size, BOOL mac) {
    NSBitmapImageRep *bitmap = [[NSBitmapImageRep alloc] initWithBitmapDataPlanes:NULL
                                                                       pixelsWide:size
                                                                       pixelsHigh:size
                                                                    bitsPerSample:8
                                                                  samplesPerPixel:4
                                                                         hasAlpha:YES
                                                                         isPlanar:NO
                                                                   colorSpaceName:NSDeviceRGBColorSpace
                                                                      bytesPerRow:0
                                                                     bitsPerPixel:0];
    [NSGraphicsContext saveGraphicsState];
    NSGraphicsContext.currentContext = [NSGraphicsContext graphicsContextWithBitmapImageRep:bitmap];
    CGFloat unit = size / 1024;

    // On a Mac, Apple's icon grid: an 824pt rounded square centred on the
    // 1024pt canvas, with a soft drop shadow. On Windows the tile fills the
    // square.
    NSRect plate = mac ? NSMakeRect(100 * unit, 100 * unit, 824 * unit, 824 * unit) : NSMakeRect(0, 0, size, size);
    CGFloat radius = (mac ? 185 : 200) * unit;
    NSBezierPath *shape = [NSBezierPath bezierPathWithRoundedRect:plate xRadius:radius yRadius:radius];
    if (mac) {
        [NSGraphicsContext saveGraphicsState];
        NSShadow *shadow = [NSShadow new];
        shadow.shadowColor = [NSColor colorWithWhite:0 alpha:0.3];
        shadow.shadowOffset = NSMakeSize(0, -12 * unit);
        shadow.shadowBlurRadius = 28 * unit;
        [shadow set];
        [NSColor.whiteColor setFill];
        [shape fill];
        [NSGraphicsContext restoreGraphicsState];
    }

    // The same two blues as Photo Organizer's icon.
    NSGradient *gradient = [[NSGradient alloc] initWithStartingColor:[NSColor colorWithSRGBRed:0.36 green:0.72 blue:1.00 alpha:1]
                                                         endingColor:[NSColor colorWithSRGBRed:0.20 green:0.30 blue:0.86 alpha:1]];
    [gradient drawInBezierPath:shape angle:-90];

    NSImageSymbolConfiguration *configuration = [NSImageSymbolConfiguration configurationWithPointSize:400 weight:NSFontWeightMedium];
    configuration = [configuration configurationByApplyingConfiguration:
                     [NSImageSymbolConfiguration configurationWithPaletteColors:@[NSColor.whiteColor]]];
    NSImage *symbol = [[NSImage imageWithSystemSymbolName:@"music.note.list" accessibilityDescription:nil]
                       imageWithSymbolConfiguration:configuration];
    NSSize symbolSize = symbol.size;
    CGFloat box = (mac ? 540 : 640) * unit;
    CGFloat scale = MIN(box / symbolSize.width, box / symbolSize.height);
    NSSize drawn = NSMakeSize(symbolSize.width * scale, symbolSize.height * scale);
    [symbol drawInRect:NSMakeRect(NSMidX(plate) - drawn.width / 2, NSMidY(plate) - drawn.height / 2, drawn.width, drawn.height)
              fromRect:NSZeroRect
             operation:NSCompositingOperationSourceOver
              fraction:1];

    [NSGraphicsContext.currentContext flushGraphics];
    [NSGraphicsContext restoreGraphicsState];
    return bitmap;
}

static NSData *PNG(NSBitmapImageRep *bitmap) {
    return [bitmap representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
}

// An .ico holding each size as a PNG, which every Windows since Vista reads.
static NSData *ICO(NSArray<NSNumber *> *sizes) {
    NSMutableArray<NSData *> *images = [NSMutableArray array];
    for (NSNumber *size in sizes) [images addObject:PNG(Draw(size.doubleValue, NO))];

    NSMutableData *out = [NSMutableData data];
    uint16_t header[3] = {0, CFSwapInt16HostToLittle(1), CFSwapInt16HostToLittle((uint16_t)images.count)};
    [out appendBytes:header length:sizeof header];
    uint32_t offset = 6 + 16 * (uint32_t)images.count;
    for (NSUInteger i = 0; i < images.count; i++) {
        uint8_t dimension = sizes[i].intValue >= 256 ? 0 : (uint8_t)sizes[i].intValue;
        uint8_t entry[16] = {dimension, dimension, 0, 0};
        uint16_t planes = CFSwapInt16HostToLittle(1), bits = CFSwapInt16HostToLittle(32);
        uint32_t length = CFSwapInt32HostToLittle((uint32_t)images[i].length), start = CFSwapInt32HostToLittle(offset);
        memcpy(entry + 4, &planes, 2);
        memcpy(entry + 6, &bits, 2);
        memcpy(entry + 8, &length, 4);
        memcpy(entry + 12, &start, 4);
        [out appendBytes:entry length:sizeof entry];
        offset += (uint32_t)images[i].length;
    }
    for (NSData *image in images) [out appendData:image];
    return out;
}

int main(int argc, const char *argv[]) {
    @autoreleasepool {
        if (argc < 2) {
            fprintf(stderr, "usage: make-icon <packaging folder>\n");
            return 1;
        }
        NSString *folder = @(argv[1]);
        NSString *windows = [folder stringByAppendingPathComponent:@"windows"];
        [NSFileManager.defaultManager createDirectoryAtPath:windows withIntermediateDirectories:YES attributes:nil error:NULL];

        BOOL ok = [PNG(Draw(1024, YES)) writeToFile:[folder stringByAppendingPathComponent:@"appicon.png"] atomically:YES]
               && [ICO(@[@256, @128, @64, @48, @32, @24, @16]) writeToFile:[windows stringByAppendingPathComponent:@"icon.ico"] atomically:YES];
        if (ok) printf("wrote appicon.png and windows/icon.ico\n");
        return ok ? 0 : 1;
    }
}
