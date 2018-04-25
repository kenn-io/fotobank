import hashlib
import os


FILE_TYPES = {
    'ARW': 'raw',
    'RAF': 'raw',
    'JPG': 'exif',
    'JPEG': 'exif',
    'GIF': 'exif',
    'DNG': 'raw',
    'CR2': 'raw'
}


def get_checksum(path, kind='md5'):
    if kind != 'md5':
        raise ValueError(kind)

    hasher = hashlib.md5()

    with open(path, 'rb') as f:
        data = f.read()
        hasher.update(data)

    return hasher.hexdigest()


def get_file_extension(path):
    root, ext = os.path.splitext(path)
    if len(ext) > 0 and ext[0] == '.':
        return ext[1:].upper()
    else:
        return ext.upper()


def get_type_from_ext(extension):
    extension = extension.upper()
    if extension in FILE_TYPES:
        return FILE_TYPES[extension]
    return 'unknown'


def discover_photos(directory_path):
    """
    Return generator yielding all paths that appear to be photos
    """
    for dirpath, dirnames, filenames in os.walk(directory_path):
        for filename in filenames:
            if path_is_photo(filename):
                yield os.path.join(dirpath, filename)


def path_is_photo(path):
    ext = get_file_extension(path)
    file_type = get_type_from_ext(ext)
    return file_type != 'unknown'
