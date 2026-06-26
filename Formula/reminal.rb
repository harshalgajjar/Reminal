class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.9.1"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.9.1/reminal_0.9.1_darwin_arm64.tar.gz"
      sha256 "c6e214229be17b88bb9e14897bb46104f23f187528e78d43b1cdfe8e30508a3a"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.9.1/reminal_0.9.1_darwin_amd64.tar.gz"
      sha256 "b00d2ffa7eb5d6e82957ab464cf82574c0e4e87f4ccc9904c4009956082861c5"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.9.1/reminal_0.9.1_linux_arm64.tar.gz"
      sha256 "35968ef4d707b93ae92558610ba735220a97eee5a3cb280daba1bd4af5d6cfbe"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.9.1/reminal_0.9.1_linux_amd64.tar.gz"
      sha256 "83b3db4af24e7361a9b0759b8c23e8adfcf10c8561c74573bdad98c05f0f7ccc"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
